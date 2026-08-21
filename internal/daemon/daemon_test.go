package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shaktsin/biewer/internal/model"
	"github.com/shaktsin/biewer/internal/procscan"
	"github.com/shaktsin/biewer/internal/telemetry"
	"github.com/shaktsin/biewer/internal/usage"
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

func TestRefreshUsagePersistsDashboardForDatabaseReaders(t *testing.T) {
	root := t.TempDir()
	transcripts := filepath.Join(root, "claude")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `{"sessionId":"s1","cwd":"/work/app","message":{"id":"m1","role":"assistant","usage":{"input_tokens":100,"cache_read_input_tokens":50,"output_tokens":25}}}` + "\n"
	if err := os.WriteFile(filepath.Join(transcripts, "s1.jsonl"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := New(Config{
		Dir: root, Scanner: fakeScanner{snap: procscan.Snapshot{Processes: map[int]procscan.RawProcess{}}},
		UsageConfig: usage.Config{ClaudeRoot: transcripts, CodexRoots: []string{filepath.Join(root, "codex")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.refreshUsage(context.Background())

	snapshot := d.PersistedSnapshot()
	if snapshot.Storage == "" || len(snapshot.ProjectUsage) != 1 || snapshot.ProjectUsage[0].Usage.TotalTokens != 175 {
		t.Fatalf("persisted dashboard missing token usage: %+v", snapshot)
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ResourceScope != model.ResourceNone || snapshot.Sessions[0].Usage.TotalTokens != 175 {
		t.Fatalf("transcript was not promoted to a logical session: %+v", snapshot.Sessions)
	}
}

func TestTranscriptTaskAndDesktopResourcesRemainSeparate(t *testing.T) {
	root := t.TempDir()
	codexRoot := filepath.Join(root, "codex")
	if err := os.MkdirAll(codexRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload := `{"timestamp":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","type":"session_meta","payload":{"id":"thread-1","cwd":"/work/app"}}` + "\n" +
		`{"timestamp":"` + now.Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10,"reasoning_output_tokens":2,"total_tokens":110}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(codexRoot, "thread-1.jsonl"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	processes := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		500: {PID: 500, PPID: 1, Command: "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT", RSSBytes: 100},
		501: {PID: 501, PPID: 500, Command: "ChatGPT Helper", RSSBytes: 50},
	}}
	d, err := New(Config{
		Dir: root, Scanner: fakeScanner{snap: processes},
		UsageConfig: usage.Config{ClaudeRoot: filepath.Join(root, "claude"), CodexRoots: []string{codexRoot}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.refreshUsage(context.Background())

	sessions := d.Snapshot().Sessions
	if len(sessions) != 2 {
		t.Fatalf("expected one logical task and one shared resource row, got %+v", sessions)
	}
	if sessions[0].Session.ID != "thread-1" || sessions[0].ResourceScope != model.ResourceNone || sessions[0].Usage.TotalTokens != 110 || sessions[0].ProcessCount != 0 {
		t.Fatalf("wrong logical task: %+v", sessions[0])
	}
	if sessions[1].Session.Agent != model.AgentChatGPT || sessions[1].ResourceScope != model.ResourceShared || sessions[1].ProcessCount != 2 || sessions[1].MemoryBytes != 150 {
		t.Fatalf("wrong shared desktop resource row: %+v", sessions[1])
	}
}

func TestIngestTelemetryPersistsAndDeduplicatesRetriedBatch(t *testing.T) {
	d := newTestDaemon(t, procscan.Snapshot{Processes: map[int]procscan.RawProcess{}})
	now := time.Now()
	events := []telemetry.Event{
		{SessionID: "thread-1", TurnID: "turn-1", Timestamp: now, Cwd: "/work/app", Model: "gpt-5", Usage: model.TokenUsage{TotalTokens: 10}, Fingerprint: "event-a"},
		{SessionID: "thread-1", TurnID: "turn-2", Timestamp: now.Add(time.Second), Usage: model.TokenUsage{TotalTokens: 20}, Fingerprint: "event-b"},
	}
	if err := d.IngestTelemetry(events); err != nil {
		t.Fatal(err)
	}
	if err := d.IngestTelemetry(events); err != nil {
		t.Fatal(err)
	}

	sessions := d.PersistedSnapshot().Sessions
	if len(sessions) != 1 || sessions[0].Session.ID != "thread-1" || sessions[0].Session.Project != "app" || sessions[0].Usage.TotalTokens != 30 || sessions[0].ResourceScope != model.ResourceNone {
		t.Fatalf("telemetry session was not persisted/deduplicated: %+v", sessions)
	}
	persisted, err := d.state.TelemetrySessions()
	if err != nil || len(persisted) != 1 || persisted[0].Usage.TotalTokens != 30 {
		t.Fatalf("telemetry state missing: got=%+v err=%v", persisted, err)
	}
}

func TestIngestTelemetryKeepsClaudeAndCodexSessionsSeparate(t *testing.T) {
	d := newTestDaemon(t, procscan.Snapshot{Processes: map[int]procscan.RawProcess{}})
	now := time.Now()
	events := []telemetry.Event{
		{Agent: model.AgentCodex, SessionID: "same-id", Timestamp: now, Usage: model.TokenUsage{TotalTokens: 10}, Fingerprint: "codex-event"},
		{Agent: model.AgentClaude, SessionID: "same-id", Timestamp: now, Model: "claude-sonnet-5", Usage: model.TokenUsage{InputTokens: 20, TotalTokens: 20}, Metrics: model.AgentMetrics{CostUSD: 0.01, ActiveSeconds: 5}, Fingerprint: "claude-event"},
	}
	if err := d.IngestTelemetry(events); err != nil {
		t.Fatal(err)
	}
	sessions := d.PersistedSnapshot().Sessions
	if len(sessions) != 2 {
		t.Fatalf("expected provider-qualified telemetry sessions, got %+v", sessions)
	}
	var foundClaude bool
	for _, session := range sessions {
		if session.Session.Agent == model.AgentClaude {
			foundClaude = session.Usage.InputTokens == 20 && session.Metrics.CostUSD == 0.01 && session.Metrics.ActiveSeconds == 5
		}
	}
	if !foundClaude {
		t.Fatalf("Claude telemetry was not retained: %+v", sessions)
	}
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

func TestTick_AutoDiscoversUnhookedDesktopApp(t *testing.T) {
	snap := procscan.Snapshot{
		Processes: map[int]procscan.RawProcess{
			500: {PID: 500, PPID: 1, Command: "/Applications/Claude.app/Contents/MacOS/Claude", RSSBytes: 100_000_000},
			501: {PID: 501, PPID: 500, Command: "/Applications/Claude.app/Contents/Frameworks/Claude Helper (Renderer).app/Contents/MacOS/Claude Helper (Renderer)", RSSBytes: 50_000_000},
		},
	}
	d := newTestDaemon(t, snap)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	snapshot := d.Snapshot()
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("expected 1 auto-discovered session with no hooks installed at all, got %d: %+v", len(snapshot.Sessions), snapshot.Sessions)
	}
	ss := snapshot.Sessions[0]
	if ss.Session.Source != model.SourceAuto {
		t.Errorf("expected Source=auto, got %s", ss.Session.Source)
	}
	if ss.Session.Agent != model.AgentClaudeDesktop {
		t.Errorf("expected AgentClaudeDesktop, got %s", ss.Session.Agent)
	}
	if ss.ProcessCount != 2 {
		t.Errorf("expected the main process + its helper both attributed, got %d", ss.ProcessCount)
	}
	if !ss.Session.LastActivityAt.IsZero() {
		t.Errorf("expected zero LastActivityAt for an auto-discovered session (no hook signal), got %v", ss.Session.LastActivityAt)
	}
	for _, f := range ss.Findings {
		if f.Severity == model.SeverityWarn {
			t.Errorf("auto-discovered sessions must never produce an idle WARN (no activity evidence exists): %+v", f)
		}
	}

	// Auto-discovered sessions must never be persisted to history.
	stored, err := d.Store().RecentSessions(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Errorf("expected auto-discovered sessions to never be persisted to history, found %+v", stored)
	}
}

func TestTick_AutoDiscoveryDoesNotDuplicateHookTrackedSession(t *testing.T) {
	snap := procscan.Snapshot{
		Processes: map[int]procscan.RawProcess{
			600: {PID: 600, PPID: 1, Command: "/Users/me/.local/bin/claude"},
			601: {PID: 601, PPID: 600, Command: "npm run dev"},
		},
	}
	d := newTestDaemon(t, snap)
	ctx := context.Background()
	// A hook already announced this exact pid as a real session.
	if err := d.HandleEvent(ctx, model.Event{Kind: model.EventSessionStart, SessionID: "hooked-1", Agent: model.AgentClaude, Cwd: "/tmp/proj", PID: 600, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	snapshot := d.Snapshot()
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("expected exactly 1 session (hook-tracked), not a duplicate auto-discovered one, got %d: %+v", len(snapshot.Sessions), snapshot.Sessions)
	}
	if snapshot.Sessions[0].Session.Source != model.SourceHook {
		t.Errorf("expected the surviving session to be the hook-tracked one, got %+v", snapshot.Sessions[0].Session)
	}
}

func TestTick_AutoDiscoveryDisabled(t *testing.T) {
	snap := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		700: {PID: 700, PPID: 1, Command: "/Users/me/.local/bin/claude"},
	}}
	d, err := New(Config{Dir: t.TempDir(), Scanner: fakeScanner{snap: snap}, DisableAutoDiscover: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshot := d.Snapshot(); len(snapshot.Sessions) != 0 {
		t.Errorf("expected no sessions with DisableAutoDiscover set, got %+v", snapshot.Sessions)
	}
}

func TestTick_AutoDiscoveredCLIUsesWorkingDirectoryAsProject(t *testing.T) {
	snapshot := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		800: {PID: 800, PPID: 1, Command: "/usr/local/bin/codex", Cwd: "/work/my-project"},
	}}
	d := newTestDaemon(t, snapshot)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessions := d.Snapshot().Sessions
	if len(sessions) != 1 || sessions[0].Session.Project != "my-project" || sessions[0].Session.Cwd != "/work/my-project" {
		t.Fatalf("auto CLI session did not use process cwd: %+v", sessions)
	}
}

func TestComposeSessionsUsesDeterministicTieBreakers(t *testing.T) {
	now := time.Now().UTC()
	d := &Daemon{
		cfg: Config{LogicalSessionWindow: time.Hour, LogicalSessionLimit: 50},
		processSessions: []model.SessionSnapshot{
			{Session: model.Session{ID: "z-process", Agent: model.AgentCodex, Project: "zeta"}, ResourceScope: model.ResourceOwned},
			{Session: model.Session{ID: "a-process", Agent: model.AgentCodex, Project: "alpha"}, ResourceScope: model.ResourceOwned},
		},
		telemetrySessions: map[string]model.TelemetrySession{
			"codex:z-logical": {SessionID: "z-logical", Agent: model.AgentCodex, Project: "zeta", UpdatedAt: now},
			"codex:a-logical": {SessionID: "a-logical", Agent: model.AgentCodex, Project: "alpha", UpdatedAt: now},
		},
	}

	for attempt := 0; attempt < 20; attempt++ {
		rows := d.composeSessionsLocked(now)
		got := make([]string, len(rows))
		for index, row := range rows {
			got[index] = row.Session.ID
		}
		want := []string{"a-logical", "z-logical", "a-process", "z-process"}
		if len(got) != len(want) {
			t.Fatalf("unexpected row count: got %v want %v", got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("attempt %d has unstable ordering: got %v want %v", attempt, got, want)
			}
		}
	}
}

func TestComposeSessionsCorrelatesCodexLaunchIDToCanonicalConversation(t *testing.T) {
	now := time.Now().UTC()
	d := &Daemon{
		cfg: Config{LogicalSessionWindow: time.Hour, LogicalSessionLimit: 50},
		processSessions: []model.SessionSnapshot{{
			Session:      model.Session{ID: "launch-abc", LaunchID: "launch-abc", Agent: model.AgentCodex, Project: "app", Cwd: "/work/app", RootPID: 100, StartedAt: now},
			ProcessTree:  []*model.Process{{PID: 100, Command: "codex", StartedAt: now}},
			ProcessCount: 1, MemoryBytes: 1234, ResourceScope: model.ResourceOwned, Attribution: model.AttributionConfirmed,
		}},
		telemetrySessions: map[string]model.TelemetrySession{
			"codex:conversation-1": {
				SessionID: "conversation-1", LaunchID: "launch-abc", Agent: model.AgentCodex,
				Cwd: "/work/app", Usage: model.TokenUsage{TotalTokens: 99}, StartedAt: now, UpdatedAt: now,
			},
		},
	}

	rows := d.composeSessionsLocked(now)
	if len(rows) != 1 {
		t.Fatalf("launch correlation should merge logical and process rows: %+v", rows)
	}
	row := rows[0]
	if row.Session.ID != "conversation-1" || row.Session.LaunchID != "launch-abc" || row.Usage.TotalTokens != 99 || row.MemoryBytes != 1234 {
		t.Fatalf("launch-correlated row lost logical or process data: %+v", row)
	}
	if row.Attribution != model.AttributionConfirmed {
		t.Fatalf("launch correlation should be confirmed, got %q", row.Attribution)
	}
}

func TestComposeSessionsRequiresPIDCreationTimeForTelemetryOwnership(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	processRow := model.SessionSnapshot{
		Session:      model.Session{ID: "auto-100", Agent: model.AgentCodex, RootPID: 100, StartedAt: now},
		ProcessTree:  []*model.Process{{PID: 100, Command: "codex", StartedAt: now, Children: []*model.Process{{PID: 101, PPID: 100, Command: "worker", StartedAt: now.Add(time.Second)}}}},
		ProcessCount: 2, ResourceScope: model.ResourceOwned, Attribution: model.AttributionProbable,
	}

	makeDaemon := func(createdAt time.Time) *Daemon {
		return &Daemon{
			cfg:             Config{LogicalSessionWindow: time.Hour, LogicalSessionLimit: 50},
			processSessions: []model.SessionSnapshot{processRow},
			telemetrySessions: map[string]model.TelemetrySession{
				"codex:conversation-1": {
					SessionID: "conversation-1", Agent: model.AgentCodex, ProcessPID: 101,
					ProcessCreatedAt: createdAt, StartedAt: now, UpdatedAt: now,
				},
			},
		}
	}

	matched := makeDaemon(now.Add(time.Second)).composeSessionsLocked(now)
	if len(matched) != 1 || matched[0].Session.ID != "conversation-1" || matched[0].Attribution != model.AttributionConfirmed {
		t.Fatalf("validated descendant PID should produce one confirmed row: %+v", matched)
	}

	mismatched := makeDaemon(now.Add(-time.Hour)).composeSessionsLocked(now)
	if len(mismatched) != 2 {
		t.Fatalf("reused/mismatched PID must remain a separate logical row: %+v", mismatched)
	}
}

func TestComposeSessionsMarksUniqueCwdTimeCorrelationProbable(t *testing.T) {
	now := time.Now().UTC()
	d := &Daemon{
		cfg: Config{LogicalSessionWindow: time.Hour, LogicalSessionLimit: 50},
		processSessions: []model.SessionSnapshot{{
			Session:       model.Session{ID: "wrapper-id", Agent: model.AgentCodex, Cwd: "/work/app", StartedAt: now},
			ResourceScope: model.ResourceOwned, Attribution: model.AttributionConfirmed,
		}},
		usageSources: []model.UsageSource{{
			SessionID: "conversation-1", Agent: model.AgentCodex, Cwd: "/work/app", Project: "app",
			StartedAt: now.Add(2 * time.Second), UpdatedAt: now, Usage: model.TokenUsage{TotalTokens: 10},
		}},
		telemetrySessions: map[string]model.TelemetrySession{},
	}

	rows := d.composeSessionsLocked(now)
	if len(rows) != 1 || rows[0].Session.ID != "conversation-1" || rows[0].Attribution != model.AttributionProbable {
		t.Fatalf("unique cwd/start-time evidence should produce one probable row: %+v", rows)
	}
}

func TestComposeSessionsRefusesAmbiguousCwdTimeCorrelation(t *testing.T) {
	now := time.Now().UTC()
	d := &Daemon{
		cfg: Config{LogicalSessionWindow: time.Hour, LogicalSessionLimit: 50},
		processSessions: []model.SessionSnapshot{{
			Session:       model.Session{ID: "wrapper-id", Agent: model.AgentCodex, Cwd: "/work/app", StartedAt: now},
			ResourceScope: model.ResourceOwned, Attribution: model.AttributionConfirmed,
		}},
		usageSources: []model.UsageSource{
			{SessionID: "conversation-1", Agent: model.AgentCodex, Cwd: "/work/app", StartedAt: now, UpdatedAt: now},
			{SessionID: "conversation-2", Agent: model.AgentCodex, Cwd: "/work/app", StartedAt: now, UpdatedAt: now},
		},
		telemetrySessions: map[string]model.TelemetrySession{},
	}

	rows := d.composeSessionsLocked(now)
	if len(rows) != 3 {
		t.Fatalf("ambiguous logical sessions must remain separate from the process row: %+v", rows)
	}
	for _, row := range rows {
		if row.Session.ID == "conversation-1" || row.Session.ID == "conversation-2" {
			if row.ResourceScope != model.ResourceNone || row.Attribution != model.AttributionNone {
				t.Fatalf("ambiguous logical row received process ownership: %+v", row)
			}
		}
	}
}
