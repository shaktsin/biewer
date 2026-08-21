// Package daemon implements Biewer's local supervisor process: it receives
// hook events over a Unix domain socket, periodically scans the OS process
// table, attributes processes to sessions by walking live process lineage
// from each session's recorded root PID, evaluates findings, and serves the
// resulting snapshot back to the CLI (`biewer watch` / `biewer status`).
//
// The control API uses a Unix domain socket under the user's Biewer home.
// An optional OTLP/HTTP receiver binds to loopback only so Codex can export
// logical-session telemetry without exposing Biewer's control API on TCP.
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
	"github.com/shaktsin/biewer/internal/discover"
	"github.com/shaktsin/biewer/internal/findings"
	"github.com/shaktsin/biewer/internal/model"
	"github.com/shaktsin/biewer/internal/procscan"
	"github.com/shaktsin/biewer/internal/telemetry"
	"github.com/shaktsin/biewer/internal/usage"
)

// EndedSessionGrace is how long a session stays visible in `biewer watch`
// after it ends, so the user can see its final state (e.g. "vite is still
// listening") rather than it vanishing the instant the agent exits.
const EndedSessionGrace = 5 * time.Minute

const (
	defaultLogicalSessionWindow = 24 * time.Hour
	defaultLogicalSessionLimit  = 50
)

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
	// DisableAutoDiscover turns off pattern-based session discovery
	// (Claude Desktop, ChatGPT, or a bare claude/codex with no hooks
	// installed), leaving only hook/wrapper-announced sessions. Off by
	// default: auto-discovery is what makes `biewer watch` show something
	// immediately after `enable`, with zero other setup.
	DisableAutoDiscover bool
	// AutoDiscoverPatterns overrides the auto-discovery pattern list. Nil
	// uses discover.DefaultPatterns().
	AutoDiscoverPatterns []discover.Pattern
	// Logger receives daemon diagnostics. Defaults to log.Default().
	Logger *log.Logger
	// UsageInterval is how often local Claude/Codex JSONL token counters are
	// refreshed. Defaults to 10 seconds.
	UsageInterval time.Duration
	// UsageConfig overrides transcript roots, primarily for tests.
	UsageConfig usage.Config
	// LogicalSessionWindow controls how recently a transcript or telemetry
	// task must have changed to appear in the live dashboard. Defaults to 24h.
	LogicalSessionWindow time.Duration
	// LogicalSessionLimit caps transcript/telemetry rows in the live snapshot.
	// Defaults to 50; project totals remain complete and unaffected.
	LogicalSessionLimit int
	// OTLPAddr is the loopback address for Codex OTLP/HTTP JSON logs. It
	// defaults to 127.0.0.1:4318. Set to "off" to disable the receiver.
	OTLPAddr string
}

// Daemon holds all live state for one running `biewer enable`d supervisor.
type Daemon struct {
	cfg   Config
	store *db.DB
	state *db.StateDB
	usage *usage.Collector

	mu                sync.Mutex
	sessions          map[string]*trackedSession // active + recently-ended, keyed by session ID
	snapshot          model.Snapshot             // last computed snapshot, served to GET /snapshot
	autoFirstSeen     map[int]time.Time          // pid -> first-observed time, for auto-discovered sessions' StartedAt
	processSessions   []model.SessionSnapshot    // process-owned/shared rows from the latest OS scan
	usageSources      []model.UsageSource
	telemetrySessions map[string]model.TelemetrySession

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
	if cfg.UsageInterval <= 0 {
		cfg.UsageInterval = 10 * time.Second
	}
	if cfg.LogicalSessionWindow <= 0 {
		cfg.LogicalSessionWindow = defaultLogicalSessionWindow
	}
	if cfg.LogicalSessionLimit <= 0 {
		cfg.LogicalSessionLimit = defaultLogicalSessionLimit
	}
	if cfg.OTLPAddr == "" {
		cfg.OTLPAddr = "127.0.0.1:4318"
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create biewer dir: %w", err)
	}
	store, err := db.Open(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	state, err := db.OpenState(cfg.Dir)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open dashboard state: %w", err)
	}
	persistedSources, err := state.UsageSources()
	if err != nil {
		cfg.Logger.Printf("load token usage index: %v", err)
	}
	persistedTelemetry, err := state.TelemetrySessions()
	if err != nil {
		cfg.Logger.Printf("load telemetry sessions: %v", err)
	}
	telemetrySessions := make(map[string]model.TelemetrySession, len(persistedTelemetry))
	for _, session := range persistedTelemetry {
		if session.SessionID != "" {
			telemetrySessions[sessionKey(session.Agent, session.SessionID)] = session
		}
	}

	d := &Daemon{
		cfg:               cfg,
		store:             store,
		state:             state,
		usage:             usage.New(cfg.UsageConfig, persistedSources),
		sessions:          make(map[string]*trackedSession),
		autoFirstSeen:     make(map[int]time.Time),
		usageSources:      append([]model.UsageSource(nil), persistedSources...),
		telemetrySessions: telemetrySessions,
		log:               cfg.Logger,
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
			ts = &trackedSession{Session: model.Session{ID: e.SessionID, Source: model.SourceHook}}
			d.sessions[e.SessionID] = ts
		}
		ts.Agent = e.Agent
		ts.Cwd = e.Cwd
		ts.Project = filepath.Base(e.Cwd)
		if ts.Project == "." || ts.Project == "" {
			ts.Project = "unknown"
		}
		ts.RootPID = e.PID
		if e.LaunchID != "" {
			ts.LaunchID = e.LaunchID
		}
		ts.StartedAt = e.Timestamp
		ts.LastActivityAt = e.Timestamp
		ts.State = model.StateActive
		ts.EndedAt = nil
	case model.EventSessionEnd:
		if !ok {
			ts = &trackedSession{Session: model.Session{ID: e.SessionID, StartedAt: e.Timestamp, Source: model.SourceHook}}
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
			ts = &trackedSession{Session: model.Session{ID: e.SessionID, Agent: e.Agent, StartedAt: e.Timestamp, State: model.StateActive, Source: model.SourceHook}}
			d.sessions[e.SessionID] = ts
		}
		ts.LastActivityAt = e.Timestamp
	default:
		d.mu.Unlock()
		return fmt.Errorf("unknown event kind %q", e.Kind)
	}
	if e.TranscriptPath != "" {
		ts.TranscriptPath = e.TranscriptPath
	}
	if e.Model != "" {
		ts.Model = e.Model
	}
	if e.LaunchID != "" {
		ts.LaunchID = e.LaunchID
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

// PersistedSnapshot reads the last complete dashboard state from the embedded
// state database. API clients use this path so the TUI is a database reader,
// not a view into the daemon's mutable in-memory maps.
func (d *Daemon) PersistedSnapshot() model.Snapshot {
	snapshot, err := d.state.Snapshot()
	if err == nil && !snapshot.GeneratedAt.IsZero() {
		return snapshot
	}
	if err != nil {
		d.log.Printf("read persisted dashboard: %v", err)
	}
	return d.Snapshot()
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
	attributed := make(map[int]bool) // every pid covered by a hook-tracked session, so auto-discovery never double-lists it

	for id, ts := range d.sessions {
		if ts.State == model.StateEnded && ts.EndedAt != nil && now.Sub(*ts.EndedAt) > EndedSessionGrace {
			toPrune = append(toPrune, id)
			continue
		}

		if process, ok := snap.Processes[ts.RootPID]; ok && ts.ProcessStartedAt.IsZero() {
			ts.ProcessStartedAt = process.StartedAt
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
			attributed[p.PID] = true
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
			ResourceScope:   model.ResourceOwned,
			Attribution:     model.AttributionConfirmed,
		})
	}
	for _, id := range toPrune {
		delete(d.sessions, id)
	}

	if !d.cfg.DisableAutoDiscover {
		sessionSnapshots = append(sessionSnapshots, d.autoDiscoverLocked(snap, attributed, now)...)
	}

	d.processSessions = append([]model.SessionSnapshot(nil), sessionSnapshots...)
	sessionSnapshots = d.composeSessionsLocked(now)
	d.snapshot = model.Snapshot{
		GeneratedAt: now, Sessions: sessionSnapshots,
		ProjectUsage: append([]model.ProjectUsage(nil), d.snapshot.ProjectUsage...),
		Storage:      d.state.Name(),
	}
	persistedSnapshot := d.snapshot
	d.mu.Unlock()

	if err := d.state.PutSnapshot(persistedSnapshot); err != nil {
		d.log.Printf("persist dashboard snapshot: %v", err)
	}

	for _, ts := range toPersist {
		if err := d.store.UpsertSession(ctx, ts.Session, ts.PeakMemoryBytes); err != nil {
			d.log.Printf("persist peak memory for %s: %v", ts.ID, err)
		}
	}
	return nil
}

// autoDiscoverLocked finds session roots Biewer wasn't told about by any
// hook (Claude Desktop, ChatGPT, or a bare claude/codex with no hooks
// installed) and turns each into a SessionSnapshot. Caller must hold d.mu.
//
// These sessions are never persisted to the history store: there's no
// session_start/session_end event to anchor a real lifecycle, so a session
// simply appears in the live snapshot while its root process exists and
// disappears the moment it doesn't — no EndedSessionGrace, no history
// entry. LastActivityAt is deliberately left zero, which findings.Evaluate
// already treats as "no idle evidence" — see that package's doc comment.
func (d *Daemon) autoDiscoverLocked(snap procscan.Snapshot, attributed map[int]bool, now time.Time) []model.SessionSnapshot {
	patterns := d.cfg.AutoDiscoverPatterns
	if patterns == nil {
		patterns = discover.DefaultPatterns()
	}
	roots := discover.FindRoots(snap, patterns, attributed)

	seenThisTick := make(map[int]bool, len(roots))
	var out []model.SessionSnapshot
	for _, root := range roots {
		seenThisTick[root.PID] = true
		firstSeen, ok := d.autoFirstSeen[root.PID]
		if !ok {
			firstSeen = now
			d.autoFirstSeen[root.PID] = firstSeen
		}

		session := model.Session{
			ID:               fmt.Sprintf("auto-%d", root.PID),
			Agent:            root.Pattern.Agent,
			Project:          root.Pattern.Label,
			RootPID:          root.PID,
			ProcessStartedAt: root.Process.StartedAt,
			StartedAt:        firstSeen,
			State:            model.StateActive,
			Source:           model.SourceAuto,
			// LastActivityAt intentionally zero: no hook signal exists.
		}
		if (session.Agent == model.AgentClaude || session.Agent == model.AgentCodex) && root.Process.Cwd != "" && root.Process.Cwd != string(filepath.Separator) && root.Process.Cwd != "." {
			session.Cwd = root.Process.Cwd
			session.Project = filepath.Base(root.Process.Cwd)
		}

		tree := buildTree(snap, root.PID)
		procs := findings.Flatten(tree)
		var totalRSS uint64
		var totalCPU float64
		var ports []int
		for _, p := range procs {
			totalRSS += p.RSSBytes
			totalCPU += p.CPUPct
			ports = append(ports, p.Ports...)
		}

		scope := resourceScopeForAgent(session.Agent)
		attribution := model.AttributionProbable
		if scope == model.ResourceShared {
			attribution = model.AttributionShared
		}
		out = append(out, model.SessionSnapshot{
			Session:         session,
			ProcessTree:     tree,
			ProcessCount:    len(procs),
			CPUSeconds:      totalCPU,
			MemoryBytes:     totalRSS,
			PeakMemoryBytes: totalRSS, // no persisted history to carry a true peak across ticks
			ListenPorts:     ports,
			Findings:        findings.Evaluate(session, tree, now, d.cfg.FindingsOptions),
			ResourceScope:   scope,
			Attribution:     attribution,
		})
	}

	for pid := range d.autoFirstSeen {
		if !seenThisTick[pid] {
			delete(d.autoFirstSeen, pid)
		}
	}
	return out
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
			StartedAt: rp.StartedAt,
			RSSBytes:  rp.RSSBytes, CPUPct: rp.CPUPct,
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
	go d.runUsageLoop(ctx)
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

func (d *Daemon) runUsageLoop(ctx context.Context) {
	d.refreshUsage(ctx)
	ticker := time.NewTicker(d.cfg.UsageInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshUsage(ctx)
		}
	}
}

func (d *Daemon) refreshUsage(ctx context.Context) {
	projects, sources, err := d.usage.Scan(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.log.Printf("refresh token usage: %v", err)
		}
		return
	}
	if err := d.state.PutUsageSources(sources); err != nil {
		d.log.Printf("persist token usage index: %v", err)
	}
	d.mu.Lock()
	d.usageSources = append([]model.UsageSource(nil), sources...)
	d.snapshot.ProjectUsage = projects
	d.snapshot.Sessions = d.composeSessionsLocked(time.Now())
	d.snapshot.Storage = d.state.Name()
	if d.snapshot.GeneratedAt.IsZero() {
		d.snapshot.GeneratedAt = time.Now()
	}
	snapshot := d.snapshot
	d.mu.Unlock()
	if err := d.state.PutSnapshot(snapshot); err != nil {
		d.log.Printf("persist token dashboard: %v", err)
	}
}

// IngestTelemetry merges normalized Codex OTLP log records into cumulative
// logical-session state. Duplicate delivery of the latest record is ignored.
func (d *Daemon) IngestTelemetry(events []telemetry.Event) error {
	if len(events) == 0 {
		return nil
	}
	now := time.Now()
	d.mu.Lock()
	for _, event := range events {
		if event.SessionID == "" {
			continue
		}
		agent := event.Agent
		if agent == "" || agent == model.AgentUnknown {
			agent = model.AgentCodex
		}
		key := sessionKey(agent, event.SessionID)
		session := d.telemetrySessions[key]
		if session.SessionID == "" {
			session.SessionID = event.SessionID
			session.Agent = agent
			session.StartedAt = event.Timestamp
		}
		if hasFingerprint(session, event.Fingerprint) {
			continue
		}
		if event.Cwd != "" {
			session.Cwd = event.Cwd
			session.Project = projectName(event.Cwd)
		}
		if event.Model != "" {
			session.Model = event.Model
		}
		if event.LaunchID != "" {
			session.LaunchID = event.LaunchID
		}
		if event.ProcessPID > 0 {
			session.ProcessPID = event.ProcessPID
		}
		if !event.ProcessCreatedAt.IsZero() {
			session.ProcessCreatedAt = event.ProcessCreatedAt
		}
		if !event.Timestamp.IsZero() {
			if session.StartedAt.IsZero() {
				session.StartedAt = event.Timestamp
			}
			if event.Timestamp.After(session.UpdatedAt) {
				session.UpdatedAt = event.Timestamp
			}
		}
		addTokenUsage(&session.Usage, event.Usage)
		addAgentMetrics(&session.Metrics, event.Metrics)
		session.LastFingerprint = event.Fingerprint
		session.RecentFingerprints = append(session.RecentFingerprints, event.Fingerprint)
		if len(session.RecentFingerprints) > 256 {
			session.RecentFingerprints = append([]string(nil), session.RecentFingerprints[len(session.RecentFingerprints)-256:]...)
		}
		d.telemetrySessions[key] = session
	}
	d.snapshot.Sessions = d.composeSessionsLocked(now)
	d.snapshot.GeneratedAt = now
	d.snapshot.Storage = d.state.Name()
	snapshot := d.snapshot
	persisted := make([]model.TelemetrySession, 0, len(d.telemetrySessions))
	for _, session := range d.telemetrySessions {
		persisted = append(persisted, session)
	}
	d.mu.Unlock()

	sort.Slice(persisted, func(i, j int) bool { return persisted[i].SessionID < persisted[j].SessionID })
	if err := d.state.PutTelemetrySessions(persisted); err != nil {
		return fmt.Errorf("persist telemetry sessions: %w", err)
	}
	if err := d.state.PutSnapshot(snapshot); err != nil {
		return fmt.Errorf("persist telemetry dashboard: %w", err)
	}
	return nil
}

func hasFingerprint(session model.TelemetrySession, fingerprint string) bool {
	if fingerprint == "" {
		return false
	}
	if session.LastFingerprint == fingerprint {
		return true
	}
	for _, seen := range session.RecentFingerprints {
		if seen == fingerprint {
			return true
		}
	}
	return false
}

func (d *Daemon) composeSessionsLocked(now time.Time) []model.SessionSnapshot {
	combined := append([]model.SessionSnapshot(nil), d.processSessions...)
	// Process-backed rows originate from maps and OS discovery. Sort them
	// before composition so identical scans always produce identical row
	// positions instead of inheriting Go's randomized map iteration order.
	sort.Slice(combined, func(i, j int) bool {
		return stableSessionLess(combined[i], combined[j])
	})
	processByID := make(map[string]int, len(combined))
	processByLaunch := make(map[string]int, len(combined))
	processByPID := make(map[int]int)
	processStartByPID := make(map[int]time.Time)
	for i := range combined {
		processByID[sessionKey(combined[i].Session.Agent, combined[i].Session.ID)] = i
		if combined[i].Session.LaunchID != "" {
			processByLaunch[sessionKey(combined[i].Session.Agent, combined[i].Session.LaunchID)] = i
		}
		for _, process := range findings.Flatten(combined[i].ProcessTree) {
			processByPID[process.PID] = i
			processStartByPID[process.PID] = process.StartedAt
		}
	}
	matchedProcesses := make(map[int]bool)
	usageSources := usage.DeduplicateSources(d.usageSources)
	probableMatches := buildProbableProcessMatches(combined, d.telemetrySessions, usageSources, now, d.cfg.LogicalSessionWindow)

	logical := make(map[string]model.SessionSnapshot)
	for _, telemetrySession := range d.telemetrySessions {
		if !recentEnough(telemetrySession.UpdatedAt, now, d.cfg.LogicalSessionWindow) {
			continue
		}
		key := sessionKey(telemetrySession.Agent, telemetrySession.SessionID)
		index, matched := processByID[key]
		confidence := model.AttributionConfirmed
		canonicalize := false
		if !matched && telemetrySession.LaunchID != "" {
			index, matched = processByLaunch[sessionKey(telemetrySession.Agent, telemetrySession.LaunchID)]
			canonicalize = matched
		}
		if !matched && validTelemetryProcessIdentity(telemetrySession, combined, processByPID, processStartByPID) {
			index = processByPID[telemetrySession.ProcessPID]
			if !matchedProcesses[index] {
				matched = true
				canonicalize = true
			}
		}
		if !matched {
			index, matched = probableMatches[key]
			if matched && !matchedProcesses[index] {
				confidence = model.AttributionProbable
				canonicalize = true
			} else {
				matched = false
			}
		}
		if matched {
			mergeTelemetry(&combined[index], telemetrySession)
			if canonicalize {
				canonicalizeProcessSession(&combined[index], telemetrySession.SessionID, telemetrySession.LaunchID)
			}
			setAttributionConfidence(&combined[index], confidence)
			matchedProcesses[index] = true
			processByID[key] = index
			continue
		}
		logical[key] = snapshotFromTelemetry(telemetrySession)
	}
	for _, source := range usageSources {
		if source.SessionID == "" || !recentEnough(source.UpdatedAt, now, d.cfg.LogicalSessionWindow) {
			continue
		}
		key := sessionKey(source.Agent, source.SessionID)
		if index, ok := processByID[key]; ok {
			mergeTranscript(&combined[index], source)
			continue
		}
		if index, ok := probableMatches[key]; ok && !matchedProcesses[index] {
			if telemetrySession, exists := d.telemetrySessions[key]; exists {
				mergeTelemetry(&combined[index], telemetrySession)
			}
			mergeTranscript(&combined[index], source)
			canonicalizeProcessSession(&combined[index], source.SessionID, "")
			setAttributionConfidence(&combined[index], model.AttributionProbable)
			matchedProcesses[index] = true
			processByID[key] = index
			delete(logical, key)
			continue
		}
		entry := logical[key]
		if entry.Session.ID == "" {
			entry = snapshotFromTranscript(source)
		} else {
			mergeTranscript(&entry, source)
		}
		logical[key] = entry
	}

	logicalRows := make([]model.SessionSnapshot, 0, len(logical))
	for _, session := range logical {
		logicalRows = append(logicalRows, session)
	}
	sort.Slice(logicalRows, func(i, j int) bool {
		left := logicalRows[i].Session.LastActivityAt
		right := logicalRows[j].Session.LastActivityAt
		if !left.Equal(right) {
			return left.After(right)
		}
		return stableSessionLess(logicalRows[i], logicalRows[j])
	})
	if len(logicalRows) > d.cfg.LogicalSessionLimit {
		logicalRows = logicalRows[:d.cfg.LogicalSessionLimit]
	}
	combined = append(logicalRows, combined...)
	return combined
}

func stableSessionLess(left, right model.SessionSnapshot) bool {
	if left.ResourceScope != right.ResourceScope {
		return left.ResourceScope < right.ResourceScope
	}
	if left.Session.Project != right.Session.Project {
		return left.Session.Project < right.Session.Project
	}
	if left.Session.Agent != right.Session.Agent {
		return left.Session.Agent < right.Session.Agent
	}
	return left.Session.ID < right.Session.ID
}

func validTelemetryProcessIdentity(session model.TelemetrySession, rows []model.SessionSnapshot, ownerByPID map[int]int, startByPID map[int]time.Time) bool {
	if session.ProcessPID <= 0 || session.ProcessCreatedAt.IsZero() {
		return false
	}
	index, ok := ownerByPID[session.ProcessPID]
	if !ok || index < 0 || index >= len(rows) {
		return false
	}
	row := rows[index]
	if row.ResourceScope != model.ResourceOwned || row.Session.Agent != session.Agent {
		return false
	}
	observedStart := startByPID[session.ProcessPID]
	if observedStart.IsZero() {
		return false
	}
	delta := observedStart.Sub(session.ProcessCreatedAt)
	if delta < 0 {
		delta = -delta
	}
	return delta <= 2*time.Second
}

func findProbableProcess(rows []model.SessionSnapshot, alreadyMatched map[int]bool, agent model.Agent, cwd string, startedAt time.Time) (int, bool) {
	if cwd == "" || startedAt.IsZero() {
		return 0, false
	}
	wantedCwd := filepath.Clean(cwd)
	candidate := -1
	for index, row := range rows {
		if alreadyMatched[index] || row.ResourceScope != model.ResourceOwned || row.Session.Agent != agent || row.Session.Cwd == "" {
			continue
		}
		if filepath.Clean(row.Session.Cwd) != wantedCwd {
			continue
		}
		delta := row.Session.StartedAt.Sub(startedAt)
		if delta < 0 {
			delta = -delta
		}
		if delta > 45*time.Second {
			continue
		}
		if candidate >= 0 {
			return 0, false // ambiguous: more than one launch fits the same cwd/time window
		}
		candidate = index
	}
	return candidate, candidate >= 0
}

type probableProcessHint struct {
	agent     model.Agent
	cwd       string
	startedAt time.Time
}

func buildProbableProcessMatches(rows []model.SessionSnapshot, telemetrySessions map[string]model.TelemetrySession, sources map[string]model.UsageSource, now time.Time, window time.Duration) map[string]int {
	hints := make(map[string]probableProcessHint)
	for _, session := range telemetrySessions {
		if recentEnough(session.UpdatedAt, now, window) {
			hints[sessionKey(session.Agent, session.SessionID)] = probableProcessHint{agent: session.Agent, cwd: session.Cwd, startedAt: session.StartedAt}
		}
	}
	for _, source := range sources {
		if source.SessionID == "" || !recentEnough(source.UpdatedAt, now, window) {
			continue
		}
		key := sessionKey(source.Agent, source.SessionID)
		hint := hints[key]
		hint.agent = source.Agent
		if source.Cwd != "" {
			hint.cwd = source.Cwd
		}
		if !source.StartedAt.IsZero() {
			hint.startedAt = source.StartedAt
		}
		hints[key] = hint
	}

	proposals := make(map[string]int)
	ownerCounts := make(map[int]int)
	for key, hint := range hints {
		if index, ok := findProbableProcess(rows, nil, hint.agent, hint.cwd, hint.startedAt); ok {
			proposals[key] = index
			ownerCounts[index]++
		}
	}
	confirmed := make(map[string]int)
	for key, index := range proposals {
		if ownerCounts[index] == 1 {
			confirmed[key] = index
		}
	}
	return confirmed
}

func canonicalizeProcessSession(target *model.SessionSnapshot, canonicalID, launchID string) {
	if launchID != "" {
		target.Session.LaunchID = launchID
	}
	if canonicalID != "" {
		target.Session.ID = canonicalID
	}
}

func setAttributionConfidence(target *model.SessionSnapshot, confidence model.AttributionConfidence) {
	switch target.ResourceScope {
	case model.ResourceShared:
		target.Attribution = model.AttributionShared
	case model.ResourceNone:
		target.Attribution = model.AttributionNone
	default:
		target.Attribution = confidence
	}
}

func snapshotFromTranscript(source model.UsageSource) model.SessionSnapshot {
	state := model.StateActive
	if source.Archived {
		state = model.StateEnded
	}
	return model.SessionSnapshot{
		Session: model.Session{
			ID: source.SessionID, Agent: source.Agent, Project: source.Project,
			Cwd: source.Cwd, Model: source.Model, StartedAt: source.StartedAt,
			LastActivityAt: source.UpdatedAt, State: state, Source: model.SourceTranscript,
			TranscriptPath: source.Path,
		},
		Usage: source.Usage, UsageUpdatedAt: source.UpdatedAt, ResourceScope: model.ResourceNone, Attribution: model.AttributionNone,
	}
}

func snapshotFromTelemetry(session model.TelemetrySession) model.SessionSnapshot {
	project := session.Project
	if project == "" {
		project = "Codex task"
	}
	return model.SessionSnapshot{
		Session: model.Session{
			ID: session.SessionID, Agent: session.Agent, Project: project,
			Cwd: session.Cwd, Model: session.Model, StartedAt: session.StartedAt,
			LastActivityAt: session.UpdatedAt, State: model.StateActive, Source: model.SourceTelemetry,
		},
		Usage: session.Usage, UsageUpdatedAt: session.UpdatedAt, Metrics: session.Metrics, ResourceScope: model.ResourceNone, Attribution: model.AttributionNone,
	}
}

func mergeTranscript(target *model.SessionSnapshot, source model.UsageSource) {
	target.Usage = source.Usage
	target.UsageUpdatedAt = source.UpdatedAt
	if target.Session.Cwd == "" {
		target.Session.Cwd = source.Cwd
		target.Session.Project = source.Project
	}
	if target.Session.Model == "" {
		target.Session.Model = source.Model
	}
	if target.Session.TranscriptPath == "" {
		target.Session.TranscriptPath = source.Path
	}
	if source.UpdatedAt.After(target.Session.LastActivityAt) {
		target.Session.LastActivityAt = source.UpdatedAt
	}
	if target.ResourceScope == model.ResourceNone {
		target.Session.Source = model.SourceTranscript
	}
}

func mergeTelemetry(target *model.SessionSnapshot, source model.TelemetrySession) {
	target.Usage = source.Usage
	target.Metrics = source.Metrics
	target.UsageUpdatedAt = source.UpdatedAt
	if target.Session.Cwd == "" && source.Cwd != "" {
		target.Session.Cwd = source.Cwd
		target.Session.Project = source.Project
	}
	if target.Session.Model == "" {
		target.Session.Model = source.Model
	}
	if source.UpdatedAt.After(target.Session.LastActivityAt) {
		target.Session.LastActivityAt = source.UpdatedAt
	}
}

func recentEnough(updatedAt, now time.Time, window time.Duration) bool {
	return !updatedAt.IsZero() && !updatedAt.Before(now.Add(-window))
}

func sessionKey(agent model.Agent, id string) string { return string(agent) + ":" + id }

func resourceScopeForAgent(agent model.Agent) model.ResourceScope {
	switch agent {
	case model.AgentClaudeDesktop, model.AgentChatGPT, model.AgentCodexDesktop:
		return model.ResourceShared
	default:
		return model.ResourceOwned
	}
}

func projectName(cwd string) string {
	name := filepath.Base(filepath.Clean(cwd))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "unknown"
	}
	return name
}

func addTokenUsage(total *model.TokenUsage, usage model.TokenUsage) {
	total.InputTokens += usage.InputTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.CacheWriteTokens += usage.CacheWriteTokens
	total.OutputTokens += usage.OutputTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
}

func addAgentMetrics(total *model.AgentMetrics, metrics model.AgentMetrics) {
	total.CostUSD += metrics.CostUSD
	total.ActiveSeconds += metrics.ActiveSeconds
	total.LinesAdded += metrics.LinesAdded
	total.LinesRemoved += metrics.LinesRemoved
	total.Commits += metrics.Commits
	total.PullRequests += metrics.PullRequests
}

// Close releases daemon resources (the history store).
func (d *Daemon) Close() error {
	stateErr := d.state.Close()
	storeErr := d.store.Close()
	if stateErr != nil {
		return stateErr
	}
	return storeErr
}
