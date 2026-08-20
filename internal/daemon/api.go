package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"syscall"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

// --- Server -----------------------------------------------------------

// Serve starts the HTTP API listening on a Unix domain socket at
// SocketPath(d.cfg.Dir), and runs the periodic scan loop. Blocks until ctx
// is cancelled or the listener errors.
func (d *Daemon) Serve(ctx context.Context) error {
	sockPath := SocketPath(d.cfg.Dir)
	_ = removeStaleSocket(sockPath)

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	defer l.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", d.handleHealthz)
	mux.HandleFunc("/events", d.handleEvents)
	mux.HandleFunc("/snapshot", d.handleSnapshot)
	mux.HandleFunc("/sessions", d.handleSessions)
	mux.HandleFunc("/sessions/stop", d.handleStop)

	srv := &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(l) }()

	go d.RunLoop(ctx)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func removeStaleSocket(path string) error {
	// A leftover socket file from an unclean shutdown makes bind fail with
	// "address already in use" even though nothing is listening. Biewer's
	// CLI guards against two daemons running via a pidfile lock (see `biewer
	// enable`) before Serve is ever called, so it's safe to unconditionally
	// clear a stale socket file here; "not exist" is not an error.
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *Daemon) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var e model.Event
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&e); err != nil {
		http.Error(w, fmt.Sprintf("bad event: %v", err), http.StatusBadRequest)
		return
	}
	if err := d.HandleEvent(r.Context(), e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Snapshot())
}

func (d *Daemon) handleSessions(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	recent, err := d.store.RecentSessions(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, recent)
}

// StopRequest is the body for POST /sessions/stop.
type StopRequest struct {
	ID  string `json:"id"`
	Dry bool   `json:"dry"`
}

func (d *Daemon) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req StopRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}

	plan, err := d.Plan(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !req.Dry {
		plan.Killed = d.Kill(plan)
	}
	writeJSON(w, http.StatusOK, plan)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- Plan / Kill --------------------------------------------------------

// PlanPID is one process Biewer proposes to terminate.
type PlanPID struct {
	PID      int    `json:"pid"`
	Command  string `json:"command"`
	RSSBytes uint64 `json:"rss_bytes"`
}

// StopPlan is the safe-cleanup plan for one session: every currently
// attributed process that would be signaled.
type StopPlan struct {
	SessionID string    `json:"session_id"`
	Project   string    `json:"project"`
	Pids      []PlanPID `json:"pids"`
	Killed    []int     `json:"killed,omitempty"`
}

// Plan resolves idOrPrefix to a live tracked session and returns the set of
// currently-attributed processes it would terminate. It does not kill
// anything.
func (d *Daemon) Plan(idOrPrefix string) (StopPlan, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var found *model.SessionSnapshot
	for i := range d.snapshot.Sessions {
		s := &d.snapshot.Sessions[i]
		if s.Session.ID == idOrPrefix {
			found = s
			break
		}
	}
	if found == nil {
		for i := range d.snapshot.Sessions {
			s := &d.snapshot.Sessions[i]
			if len(idOrPrefix) >= 4 && len(s.Session.ID) >= len(idOrPrefix) && s.Session.ID[:len(idOrPrefix)] == idOrPrefix {
				if found != nil {
					return StopPlan{}, fmt.Errorf("session id prefix %q is ambiguous", idOrPrefix)
				}
				found = s
			}
		}
	}
	if found == nil {
		return StopPlan{}, fmt.Errorf("no live session matching %q (already ended? try `biewer sessions`)", idOrPrefix)
	}

	plan := StopPlan{SessionID: found.Session.ID, Project: found.Session.Project}
	for _, p := range findingsFlattenLocal(found.ProcessTree) {
		plan.Pids = append(plan.Pids, PlanPID{PID: p.PID, Command: p.Command, RSSBytes: p.RSSBytes})
	}
	sort.Slice(plan.Pids, func(i, j int) bool { return plan.Pids[i].PID < plan.Pids[j].PID })
	return plan, nil
}

// Kill sends SIGTERM to every pid in plan, waits briefly, then SIGKILLs any
// stragglers. Returns the pids that were actually signaled successfully.
// Best-effort: a pid that already exited between planning and killing is
// not an error.
func (d *Daemon) Kill(plan StopPlan) []int {
	var signaled []int
	for _, p := range plan.Pids {
		if err := syscall.Kill(p.PID, syscall.SIGTERM); err == nil {
			signaled = append(signaled, p.PID)
		}
	}
	time.Sleep(2 * time.Second)
	for _, p := range plan.Pids {
		if isAlive(p.PID) {
			_ = syscall.Kill(p.PID, syscall.SIGKILL)
		}
	}
	return signaled
}

func isAlive(pid int) bool {
	// Signal 0 performs no-op error checking: ESRCH means the process is
	// gone, EPERM means it exists but we can't signal it (still "alive"
	// for our purposes), nil means it exists and we can.
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func findingsFlattenLocal(tree []*model.Process) []*model.Process {
	var out []*model.Process
	var walk func(*model.Process)
	walk = func(p *model.Process) {
		out = append(out, p)
		for _, c := range p.Children {
			walk(c)
		}
	}
	for _, p := range tree {
		walk(p)
	}
	return out
}

// --- Client -------------------------------------------------------------

// Client talks to a running daemon over its Unix domain socket.
type Client struct {
	sockPath string
	http     *http.Client
}

// NewClient returns a Client for the daemon socket under dir.
func NewClient(dir string) *Client {
	sockPath := SocketPath(dir)
	return &Client{
		sockPath: sockPath,
		http: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					d := net.Dialer{}
					return d.DialContext(ctx, "unix", sockPath)
				},
			},
		},
	}
}

func (c *Client) Healthz(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://biewer/healthz", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon healthz: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) PostEvent(ctx context.Context, e model.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://biewer/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post event: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) Snapshot(ctx context.Context) (model.Snapshot, error) {
	var snap model.Snapshot
	err := c.getJSON(ctx, "http://biewer/snapshot", &snap)
	return snap, err
}

func (c *Client) Sessions(ctx context.Context, limit int) ([]model.Session, error) {
	// The server returns db.StoredSession-shaped JSON; model.Session is
	// embedded so a plain decode into []model.Session drops the extra
	// peak_memory_bytes field, which callers that only need history
	// summaries (agent/project/duration) don't need. Callers wanting peak
	// memory should hit /sessions directly.
	var raw []struct {
		model.Session
		PeakMemoryBytes uint64 `json:"peak_memory_bytes"`
	}
	url := fmt.Sprintf("http://biewer/sessions?limit=%d", limit)
	if err := c.getJSON(ctx, url, &raw); err != nil {
		return nil, err
	}
	out := make([]model.Session, len(raw))
	for i, r := range raw {
		out[i] = r.Session
	}
	return out, nil
}

func (c *Client) Stop(ctx context.Context, id string, dry bool) (StopPlan, error) {
	var plan StopPlan
	b, _ := json.Marshal(StopRequest{ID: id, Dry: dry})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://biewer/sessions/stop", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return plan, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return plan, fmt.Errorf("stop: status %d: %s", resp.StatusCode, string(body))
	}
	err = json.NewDecoder(resp.Body).Decode(&plan)
	return plan, err
}

func (c *Client) getJSON(ctx context.Context, url string, v any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
