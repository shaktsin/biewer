package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/shaktsin/biewer/internal/model"
	"github.com/shaktsin/biewer/internal/procscan"
)

type fakeScanner struct{ snap procscan.Snapshot }

func (f fakeScanner) Scan(context.Context) (procscan.Snapshot, error) { return f.snap, nil }

func newTestDaemon(t *testing.T, snap procscan.Snapshot) *Daemon {
	t.Helper()
	d, err := New(Config{Dir: t.TempDir(), Scanner: fakeScanner{snap: snap}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestHandleEvent_SessionLifecycle(t *testing.T) {
	d := newTestDaemon(t, procscan.Snapshot{Processes: map[int]procscan.RawProcess{}})
	ctx := context.Background()

	start := model.Event{Kind: model.EventSessionStart, SessionID: "s1", Agent: model.AgentClaude, Cwd: "/tmp/myproj", PID: 4242, Timestamp: time.Now()}
	if err := d.HandleEvent(ctx, start); err != nil {
		t.Fatalf("HandleEvent session_start: %v", err)
	}

	d.mu.Lock()
	ts, ok := d.sessions["s1"]
	d.mu.Unlock()
	if !ok {
		t.Fatal("expected session s1 to be tracked after session_start")
	}
	if ts.RootPID != 4242 || ts.Project != "myproj" || ts.State != model.StateActive {
		t.Errorf("unexpected tracked session: %+v", ts)
	}

	tool := model.Event{Kind: model.EventPreToolUse, SessionID: "s1", ToolName: "Bash", Timestamp: time.Now().Add(time.Minute)}
	if err := d.HandleEvent(ctx, tool); err != nil {
		t.Fatalf("HandleEvent pre_tool_use: %v", err)
	}
	d.mu.Lock()
	lastActivity := d.sessions["s1"].LastActivityAt
	d.mu.Unlock()
	if !lastActivity.Equal(tool.Timestamp) {
		t.Errorf("expected LastActivityAt to update to tool event timestamp, got %v want %v", lastActivity, tool.Timestamp)
	}

	end := model.Event{Kind: model.EventSessionEnd, SessionID: "s1", Timestamp: time.Now().Add(2 * time.Minute)}
	if err := d.HandleEvent(ctx, end); err != nil {
		t.Fatalf("HandleEvent session_end: %v", err)
	}
	d.mu.Lock()
	state := d.sessions["s1"].State
	endedAt := d.sessions["s1"].EndedAt
	d.mu.Unlock()
	if state != model.StateEnded || endedAt == nil {
		t.Errorf("expected session to be ended, got state=%s endedAt=%v", state, endedAt)
	}

	// History should be queryable after the fact.
	stored, err := d.Store().GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession after lifecycle: %v", err)
	}
	if stored.State != model.StateEnded {
		t.Errorf("expected persisted session to be ended, got %+v", stored)
	}
}

func TestHandleEvent_MissingSessionID(t *testing.T) {
	d := newTestDaemon(t, procscan.Snapshot{Processes: map[int]procscan.RawProcess{}})
	err := d.HandleEvent(context.Background(), model.Event{Kind: model.EventSessionStart})
	if err == nil {
		t.Fatal("expected an error for an event with no session_id")
	}
}

func TestTick_AttributesLiveDescendantsAndFindings(t *testing.T) {
	snap := procscan.Snapshot{
		Processes: map[int]procscan.RawProcess{
			100: {PID: 100, PPID: 1, Command: "codex", RSSBytes: 10_000_000},
			101: {PID: 101, PPID: 100, Command: "npm run dev", RSSBytes: 50_000_000},
			999: {PID: 999, PPID: 1, Command: "unrelated"}, // must not be attributed
		},
		ListenPorts: map[int][]int{101: {5173}},
	}
	d := newTestDaemon(t, snap)
	ctx := context.Background()

	longAgo := time.Now().Add(-30 * time.Minute)
	if err := d.HandleEvent(ctx, model.Event{Kind: model.EventSessionStart, SessionID: "s1", Agent: model.AgentCodex, Cwd: "/tmp/proj", PID: 100, Timestamp: longAgo}); err != nil {
		t.Fatal(err)
	}

	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	snapshot := d.Snapshot()
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("expected 1 session in snapshot, got %d", len(snapshot.Sessions))
	}
	ss := snapshot.Sessions[0]
	if ss.ProcessCount != 2 {
		t.Errorf("expected 2 attributed processes, got %d", ss.ProcessCount)
	}
	if ss.MemoryBytes != 60_000_000 {
		t.Errorf("expected 60,000,000 attributed bytes, got %d", ss.MemoryBytes)
	}

	var warned bool
	for _, f := range ss.Findings {
		if f.Severity == model.SeverityWarn {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected an idle-dev-server WARN finding, got %+v", ss.Findings)
	}
}

func TestPlanAndKill(t *testing.T) {
	snap := procscan.Snapshot{
		Processes: map[int]procscan.RawProcess{
			200: {PID: 200, PPID: 1, Command: "claude", RSSBytes: 1000},
			201: {PID: 201, PPID: 200, Command: "vite", RSSBytes: 2000},
		},
	}
	d := newTestDaemon(t, snap)
	ctx := context.Background()
	if err := d.HandleEvent(ctx, model.Event{Kind: model.EventSessionStart, SessionID: "s2", Agent: model.AgentClaude, Cwd: "/tmp/x", PID: 200, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := d.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	plan, err := d.Plan("s2")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Pids) != 2 {
		t.Fatalf("expected plan to include 2 pids, got %+v", plan.Pids)
	}

	if _, err := d.Plan("does-not-exist"); err == nil {
		t.Fatal("expected an error planning a nonexistent session")
	}
}
