// Package daemon implements Biewer's local supervisor process: it receives
// hook events over a Unix domain socket, periodically scans the OS process
// table, attributes processes to sessions by walking live process lineage
// from each session's recorded root PID, evaluates findings, and serves the
// resulting snapshot back to the CLI (`biewer watch` / `biewer status`).
//
// Deliberately: no TCP port is ever opened. Everything is a Unix domain
// socket under the user's Biewer home directory, reachable only by local
// processes with filesystem access to it.
package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/shaktsin/biewer/internal/db"
	"github.com/shaktsin/biewer/internal/findings"
	"github.com/shaktsin/biewer/internal/model"
	"github.com/shaktsin/biewer/internal/procscan"
)

// EndedSessionGrace is how long a session stays visible in `biewer watch`
// after it ends, so the user can see its final state (e.g. "vite is still
// listening") rather than it vanishing the instant the agent exits.
const EndedSessionGrace = 5 * time.Minute

// Config configures a Daemon.
type Config struct {
	// Dir is Biewer's home directory (default ~/.biewer). The session
	// history store and socket both live under it.
	Dir string
	// ScanInterval is how often the process table is re-scanned and
	// findings re-evaluated. Defaults to 2s.
	ScanInterval time.Duration
	// Scanner overrides the platform process scanner. Tests inject a fake;
	// production uses procscan.NewScanner().
	Scanner procscan.Scanner
	// FindingsOptions overrides finding thresholds. Zero value uses
	// findings.DefaultOptions().
	FindingsOptions findings.Options
	// Logger receives daemon diagnostics. Defaults to log.Default().
	Logger *log.Logger
}

// Daemon holds all live state for one running `biewer enable`d supervisor.
type Daemon struct {
	cfg   Config
	store *db.DB

	mu       sync.Mutex
	sessions map[string]*trackedSession // active + recently-ended, keyed by session ID
	snapshot model.Snapshot             // last computed snapshot, served to GET /snapshot

	log *log.Logger
}

type trackedSession struct {
	model.Session
	PeakMemoryBytes uint64
}

// SocketPath returns the Unix domain socket path for a Biewer home
// directory.
func SocketPath(dir string) string { return filepath.Join(dir, "daemon.sock") }

// PidFilePath returns the pidfile path for a Biewer home directory.
func PidFilePath(dir string) string { return filepath.Join(dir, "daemon.pid") }

// LogFilePath returns the log file path for a Biewer home directory.
func LogFilePath(dir string) string { return filepath.Join(dir, "daemon.log") }

// New constructs a Daemon. Call Run to start serving.
func New(cfg Config) (*Daemon, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("daemon: Dir is required")
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = 2 * time.Second
	}
	if cfg.Scanner == nil {
		cfg.Scanner = procscan.NewScanner()
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create biewer dir: %w", err)
	}
	store, err := db.Open(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	d := &Daemon{
		cfg:      cfg,
		store:    store,
		sessions: make(map[string]*trackedSession),
		log:      cfg.Logger,
	}

	// Rehydrate any sessions that were still active when the daemon last
	// stopped, so a restart doesn't silently forget what it was watching.
	// They'll simply show 0 attributed processes if their root pid is
	// gone, and findings/`watch` will make that visually obvious.
	if recent, err := store.RecentSessions(context.Background(), 200); err == nil {
		for _, r := range recent {
			if r.State == model.StateActive {
				d.sessions[r.ID] = &trackedSession{Session: r.Session, PeakMemoryBytes: r.PeakMemoryBytes}
			}
		}
	}

	return d, nil
}

// Store exposes the underlying history store, e.g. for CLI commands that
// read history without going through the socket (`biewer sessions`).
func (d *Daemon) Store() *db.DB { return d.store }

// HandleEvent applies one hook event to daemon state: creating, updating,
// or ending a tracked session, and appending it to the audit log.
func (d *Daemon) HandleEvent(ctx context.Context, e model.Event) error {
	if e.SessionID == "" {
		return fmt.Errorf("event missing session_id")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}

	d.mu.Lock()
	ts, ok := d.sessions[e.SessionID]
	switch e.Kind {
	case model.EventSessionStart:
		if !ok {
			ts = &trackedSession{Session: model.Session{ID: e.SessionID}}
			d.sessions[e.SessionID] = ts
		}
		ts.Agent = e.Agent
		ts.Cwd = e.Cwd
		ts.Project = filepath.Base(e.Cwd)
		if ts.Project == "." || ts.Project == "" {
			ts.Project = "unknown"
		}
		ts.RootPID = e.PID
		ts.StartedAt = e.Timestamp
		ts.LastActivityAt = e.Timestamp
		ts.State = model.StateActive
		ts.EndedAt = nil
	case model.EventSessionEnd:
		if !ok {
			ts = &trackedSession{Session: model.Session{ID: e.SessionID, StartedAt: e.Timestamp}}
			d.sessions[e.SessionID] = ts
		}
		ended := e.Timestamp
		ts.EndedAt = &ended
		ts.State = model.StateEnded
		ts.LastActivityAt = e.Timestamp
	case model.EventUserPromptSubmit, model.EventPreToolUse, model.EventPostToolUse:
		if !ok {
			// Activity for a session we never saw start (e.g. daemon was
			// restarted mid-session). Track it best-effort so activity
			// timing is still meaningful even without a root pid.
			ts = &trackedSession{Session: model.Session{ID: e.SessionID, Agent: e.Agent, StartedAt: e.Timestamp, State: model.StateActive}}
			d.sessions[e.SessionID] = ts
		}
		ts.LastActivityAt = e.Timestamp
	default:
		d.mu.Unlock()
		return fmt.Errorf("unknown event kind %q", e.Kind)
	}
	sessionCopy := ts.Session
	peak := ts.PeakMemoryBytes
	d.mu.Unlock()

	if err := d.store.RecordEvent(ctx, e); err != nil {
		d.log.Printf("record event: %v", err)
	}
	if err := d.store.UpsertSession(ctx, sessionCopy, peak); err != nil {
		d.log.Printf("upsert session: %v", err)
	}
	return nil
}

// Snapshot returns the most recently computed snapshot (populated by the
// scan loop; see Tick). Safe for concurrent use.
func (d *Daemon) Snapshot() model.Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot
}

// Tick performs one scan-attribute-evaluate cycle: scans the process table,
// attributes processes to each tracked session by live lineage from its
// root PID, evaluates findings, updates the cached snapshot, persists any
// new peak-memory high-water marks, and prunes sessions that ended more
// than EndedSessionGrace ago.
func (d *Daemon) Tick(ctx context.Context) error {
	snap, err := d.cfg.Scanner.Scan(ctx)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	now := time.Now()

	d.mu.Lock()
	var sessionSnapshots []model.SessionSnapshot
	var toPersist []trackedSession
	var toPrune []string

	for id, ts := range d.sessions {
		if ts.State == model.StateEnded && ts.EndedAt != nil && now.Sub(*ts.EndedAt) > EndedSessionGrace {
			toPrune = append(toPrune, id)
			continue
		}

		tree := buildTree(snap, ts.RootPID)
		procs := findings.Flatten(tree)

		var totalRSS uint64
		var totalCPU float64
		var ports []int
		for _, p := range procs {
			totalRSS += p.RSSBytes
			totalCPU += p.CPUPct
			ports = append(ports, p.Ports...)
		}
		if totalRSS > ts.PeakMemoryBytes {
			ts.PeakMemoryBytes = totalRSS
			toPersist = append(toPersist, *ts)
		}

		opts := d.cfg.FindingsOptions
		fnds := findings.Evaluate(ts.Session, tree, now, opts)

		sessionSnapshots = append(sessionSnapshots, model.SessionSnapshot{
			Session:         ts.Session,
			ProcessTree:     tree,
			ProcessCount:    len(procs),
			CPUSeconds:      totalCPU,
			MemoryBytes:     totalRSS,
			PeakMemoryBytes: ts.PeakMemoryBytes,
			ListenPorts:     ports,
			Findings:        fnds,
		})
	}
	for _, id := range toPrune {
		delete(d.sessions, id)
	}
	sort.Slice(sessionSnapshots, func(i, j int) bool {
		return sessionSnapshots[i].Session.StartedAt.After(sessionSnapshots[j].Session.StartedAt)
	})
	d.snapshot = model.Snapshot{GeneratedAt: now, Sessions: sessionSnapshots}
	d.mu.Unlock()

	for _, ts := range toPersist {
		if err := d.store.UpsertSession(ctx, ts.Session, ts.PeakMemoryBytes); err != nil {
			d.log.Printf("persist peak memory for %s: %v", ts.ID, err)
		}
	}
	return nil
}

// buildTree walks snap's live parent/child links from root into a
// model.Process tree. Returns nil if root is not currently a running
// process (e.g. the agent hasn't been observed yet, or exited without a
// session_end event).
func buildTree(snap procscan.Snapshot, root int) []*model.Process {
	if root == 0 {
		return nil
	}
	if _, ok := snap.Processes[root]; !ok {
		return nil
	}
	children := snap.Children()
	visited := make(map[int]bool)

	var build func(pid, depth int) *model.Process
	build = func(pid, depth int) *model.Process {
		visited[pid] = true
		rp := snap.Processes[pid]
		mp := &model.Process{
			PID: pid, PPID: rp.PPID, Command: rp.Command,
			RSSBytes: rp.RSSBytes, CPUPct: rp.CPUPct,
			Ports: snap.ListenPorts[pid], Depth: depth,
		}
		kids := append([]int(nil), children[pid]...)
		sort.Ints(kids)
		for _, c := range kids {
			if visited[c] {
				continue // defensive: never loop on a malformed/cyclic snapshot
			}
			mp.Children = append(mp.Children, build(c, depth+1))
		}
		return mp
	}
	return []*model.Process{build(root, 0)}
}

// RunLoop runs Tick on cfg.ScanInterval until ctx is cancelled.
func (d *Daemon) RunLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.ScanInterval)
	defer ticker.Stop()
	// Do an immediate first tick so a freshly (re)started daemon doesn't
	// show an empty snapshot for a full interval.
	if err := d.Tick(ctx); err != nil {
		d.log.Printf("tick: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.Tick(ctx); err != nil {
				d.log.Printf("tick: %v", err)
			}
		}
	}
}

// Close releases daemon resources (the history store).
func (d *Daemon) Close() error { return d.store.Close() }
