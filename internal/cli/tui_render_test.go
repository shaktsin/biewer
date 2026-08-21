package cli

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shaktsin/biewer/internal/model"
)

func TestRenderTUIViewShowsDashboardAndSelectedSession(t *testing.T) {
	now := time.Date(2026, 8, 20, 18, 30, 0, 0, time.Local)
	view := tuiView{
		Snapshot: model.Snapshot{Storage: "rocksdb", ProjectUsage: []model.ProjectUsage{{
			Project: "my-app", Cwd: "/work/my-app", Sessions: 3,
			Usage: model.TokenUsage{InputTokens: 1200, CachedInputTokens: 5000, OutputTokens: 400, TotalTokens: 6600},
		}}, Sessions: []model.SessionSnapshot{{
			Session: model.Session{
				ID: "0195d17a-full", Agent: model.AgentCodex, Project: "my-app", Cwd: "/work/my-app",
				RootPID: 1234, Source: model.SourceHook, State: model.StateActive,
				StartedAt: now.Add(-5 * time.Minute), LastActivityAt: now.Add(-10 * time.Second),
			},
			ProcessCount: 2, CPUSeconds: 4.2, MemoryBytes: 612 * 1024 * 1024,
			Usage:          model.TokenUsage{InputTokens: 1200, CachedInputTokens: 5000, OutputTokens: 400, TotalTokens: 6600},
			UsageUpdatedAt: now, ResourceScope: model.ResourceOwned,
			ProcessTree: []*model.Process{{PID: 1234, Command: "codex", RSSBytes: 200 * 1024 * 1024}},
			Findings:    []model.Finding{{Severity: model.SeverityWarn, Message: "vite remains active"}},
		}}},
		SessionHistory: map[string][]tuiSample{"codex:0195d17a-full": {
			{At: now.Add(-4 * time.Second), CPU: 1.2, Memory: 500 * 1024 * 1024, Tokens: 6000},
			{At: now.Add(-2 * time.Second), CPU: 4.2, Memory: 612 * 1024 * 1024, Tokens: 6600},
		}},
		Width: 100, Height: 24, Selected: 0, Now: now, LastRefresh: now, Connected: true, Color: false,
	}

	got := renderTUIView(view)
	for _, want := range []string{"BIEWER", "● LIVE", "SESSION OVERVIEW", "TOKENS", "TOK/m", "█", "SESSIONS", "my-app", "0195d17a", "│", "6.6K", "PROCESS TREE", "vite remains active", "q quit"} {
		if !strings.Contains(got, want) {
			t.Errorf("dashboard missing %q:\n%s", want, got)
		}
	}
	for _, removed := range []string{"AGENT PROCESS SUPERVISOR", "DB ROCKSDB", "tokens —", "TOKEN CONSUMPTION", "Not available for this session source", "Hook/log discovery is required", "PEAK"} {
		if strings.Contains(got, removed) {
			t.Errorf("dashboard still contains removed element %q:\n%s", removed, got)
		}
	}
	if separators := strings.Count(got, "\r\n"); separators != view.Height-1 {
		t.Errorf("expected %d row separators with no trailing newline, got %d", view.Height-1, separators)
	}
	if strings.HasSuffix(got, "\r\n") {
		t.Error("dashboard must not end with a newline that scrolls the terminal")
	}
}

func TestRenderTUIViewOffline(t *testing.T) {
	view := tuiView{Width: 80, Height: 18, Now: time.Now(), LastRefresh: time.Now(), Color: false}
	got := renderTUIView(view)
	if !strings.Contains(got, "OFFLINE") || !strings.Contains(got, "biewer enable") {
		t.Fatalf("offline dashboard should explain how to start daemon:\n%s", got)
	}
}

func TestRenderTUIViewSeparatesLogicalTaskFromSharedDesktopResources(t *testing.T) {
	now := time.Now()
	view := tuiView{
		Snapshot: model.Snapshot{Sessions: []model.SessionSnapshot{
			{
				Session:       model.Session{ID: "thread-1", Agent: model.AgentClaude, Project: "app", Cwd: "/work/app", Source: model.SourceTranscript, LastActivityAt: now},
				Usage:         model.TokenUsage{InputTokens: 60, CachedInputTokens: 40, OutputTokens: 10, TotalTokens: 110},
				Metrics:       model.AgentMetrics{CostUSD: 0.0125, ActiveSeconds: 15, LinesAdded: 4, LinesRemoved: 2, Commits: 1},
				ResourceScope: model.ResourceNone,
			},
			{
				Session:      model.Session{ID: "auto-500", Agent: model.AgentChatGPT, Project: "ChatGPT Desktop", Source: model.SourceAuto},
				ProcessCount: 3, MemoryBytes: 1024, ResourceScope: model.ResourceShared,
			},
		}},
		Width: 110, Height: 24, Now: now, LastRefresh: now, Connected: true,
	}
	logical := renderTUIView(view)
	for _, want := range []string{"SESSION OVERVIEW", "TOKENS", "110", "AGENT METRICS", "$0.0125", "LINES +4/-2", "CPU/MEM stay in shared row", "1 SHARED"} {
		if !strings.Contains(logical, want) {
			t.Errorf("logical task view missing %q:\n%s", want, logical)
		}
	}

	view.Selected = 1
	shared := renderTUIView(view)
	for _, want := range []string{"SHARED RESOURCES", "Combined desktop totals", "3 PROCESSES"} {
		if !strings.Contains(shared, want) {
			t.Errorf("shared resource view missing %q:\n%s", want, shared)
		}
	}
}

func TestTUIDynamicSamplesProduceRateAndBoundHistory(t *testing.T) {
	now := time.Now()
	history := []tuiSample{{At: now, CPU: 1, Tokens: 100}}
	history = appendTUISample(history, tuiSample{At: now.Add(time.Minute), CPU: 4, Tokens: 700}, 2)
	history = appendTUISample(history, tuiSample{At: now.Add(2 * time.Minute), CPU: 2, Tokens: 1000}, 2)
	if len(history) != 2 || history[0].Tokens != 700 {
		t.Fatalf("history should retain the newest two samples: %#v", history)
	}
	rates := sampleTokenRates(history)
	if len(rates) != 1 || rates[0] != 300 {
		t.Fatalf("unexpected token rate: %#v", rates)
	}
	if graph := sparkline(sampleCPU(history), 6); !strings.Contains(graph, "█") || len([]rune(graph)) != 6 {
		t.Fatalf("unexpected sparkline: %q", graph)
	}
}

func TestSelectedOverviewUsesOnlySelectedSessionHistory(t *testing.T) {
	now := time.Now()
	first := model.SessionSnapshot{
		Session: model.Session{ID: "first"}, CPUSeconds: 10, MemoryBytes: 1024,
		Usage: model.TokenUsage{TotalTokens: 200}, ResourceScope: model.ResourceOwned,
	}
	second := model.SessionSnapshot{
		Session: model.Session{ID: "second"}, CPUSeconds: 75, MemoryBytes: 2048,
		Usage: model.TokenUsage{TotalTokens: 900}, ResourceScope: model.ResourceOwned,
	}
	view := tuiView{Now: now, SessionHistory: map[string][]tuiSample{
		":first":  {{At: now.Add(-time.Minute), CPU: 2, Tokens: 100}, {At: now, CPU: 10, Tokens: 200}},
		":second": {{At: now.Add(-time.Minute), CPU: 30, Tokens: 300}, {At: now, CPU: 75, Tokens: 900}},
	}}
	firstCard := selectedOverviewText(buildSelectedOverview(view, first, 72))
	secondCard := selectedOverviewText(buildSelectedOverview(view, second, 72))
	if !strings.Contains(firstCard, "CPU  10.0%") || !strings.Contains(firstCard, "TOK/m 100") {
		t.Fatalf("first overview did not use first session metrics:\n%s", firstCard)
	}
	if !strings.Contains(secondCard, "CPU  75.0%") || !strings.Contains(secondCard, "TOK/m 600") {
		t.Fatalf("second overview did not use second session metrics:\n%s", secondCard)
	}
}

func TestTUISelectionFollowsSessionIdentityAcrossRefreshReordering(t *testing.T) {
	first := model.SessionSnapshot{Session: model.Session{ID: "first", Agent: model.AgentCodex}}
	selected := model.SessionSnapshot{Session: model.Session{ID: "selected", Agent: model.AgentClaude}}

	index, key := reconcileTUISelection([]model.SessionSnapshot{first, selected}, "", 1)
	if index != 1 || key != "claude:selected" {
		t.Fatalf("unexpected initial selection: index=%d key=%q", index, key)
	}
	index, key = reconcileTUISelection([]model.SessionSnapshot{selected, first}, key, index)
	if index != 0 || key != "claude:selected" {
		t.Fatalf("selection did not follow identity after reorder: index=%d key=%q", index, key)
	}

	// Provider is part of the identity because different agents can expose
	// the same provider-local session ID.
	codexTwin := model.SessionSnapshot{Session: model.Session{ID: "selected", Agent: model.AgentCodex}}
	index, key = reconcileTUISelection([]model.SessionSnapshot{codexTwin, selected}, key, index)
	if index != 1 || key != "claude:selected" {
		t.Fatalf("selection confused same-ID sessions from different agents: index=%d key=%q", index, key)
	}
}

func selectedOverviewText(lines []tuiLine) string {
	parts := make([]string, len(lines))
	for index, line := range lines {
		parts[index] = line.text
	}
	return strings.Join(parts, "\n")
}

func TestFitUsesRuneWidthAndPads(t *testing.T) {
	if got := fit("biewer", 8); got != "biewer  " {
		t.Fatalf("fit padding: %q", got)
	}
	if got := fit("abcdefgh", 5); got != "abcd…" {
		t.Fatalf("fit truncation: %q", got)
	}
}

func TestShortIDKeepsFullAutoDiscoveredPID(t *testing.T) {
	if got := shortID("auto-44132"); got != "auto-44132" {
		t.Fatalf("auto-discovered PID was made ambiguous: %q", got)
	}
	if got := shortID("01a021ed-4813-7870"); got != "01a021ed" {
		t.Fatalf("UUID-style session ID should remain compact: %q", got)
	}
}

func TestRenderTUIFrameUsesAbsoluteRowsWithoutScrolling(t *testing.T) {
	view := tuiView{Width: 80, Height: 18, Now: time.Now(), LastRefresh: time.Now(), Connected: true}
	frame := renderTUIFrame(view)
	if strings.Contains(frame, "\r\n") || strings.Contains(frame, "\n") {
		t.Fatalf("frame must not contain scrolling newlines: %q", frame)
	}
	if !strings.HasPrefix(frame, "\033[?7l\033[2J\033[1;1H") {
		t.Fatalf("frame must disable wrapping, clear, and address row 1: %q", frame[:minInt(len(frame), 40)])
	}
	if got := strings.Count(frame, ";1H"); got != view.Height {
		t.Fatalf("expected one absolute cursor address per row, got %d want %d", got, view.Height)
	}
	if !strings.Contains(enterAltScreen, "\033[?7l") || !strings.Contains(leaveAltScreen, "\033[?7h") {
		t.Fatal("alternate screen lifecycle must disable and restore auto-wrap")
	}
}

func TestRichColoredTUIKeepsEveryRowAtTerminalWidth(t *testing.T) {
	now := time.Now()
	view := tuiView{
		Snapshot: model.Snapshot{Storage: "rocksdb", Sessions: []model.SessionSnapshot{{
			Session:    model.Session{ID: "session-1", Agent: model.AgentClaude, Project: "colorful-app", LastActivityAt: now},
			Usage:      model.TokenUsage{InputTokens: 2500, CachedInputTokens: 9000, OutputTokens: 800, TotalTokens: 12300},
			CPUSeconds: 14.2, MemoryBytes: 512 * 1024 * 1024, ProcessCount: 4, ResourceScope: model.ResourceOwned,
		}}},
		SessionHistory: map[string][]tuiSample{"claude:session-1": {{At: now.Add(-time.Second), CPU: 3, Tokens: 12000}, {At: now, CPU: 14.2, Tokens: 12300}}},
		Width:          120, Height: 28, Now: now, LastRefresh: now, Connected: true, Color: true,
	}
	rows := strings.Split(renderTUIView(view), "\r\n")
	if len(rows) != view.Height {
		t.Fatalf("got %d rows, want %d", len(rows), view.Height)
	}
	for index, row := range rows {
		visible := stripTUIANSI(row)
		if got := utf8.RuneCountInString(visible); got != view.Width {
			t.Fatalf("row %d has visible width %d, want %d: %q", index+1, got, view.Width, visible)
		}
	}
}

func stripTUIANSI(text string) string {
	var visible strings.Builder
	for index := 0; index < len(text); {
		if text[index] != 0x1b {
			visible.WriteByte(text[index])
			index++
			continue
		}
		index++
		if index < len(text) && text[index] == '[' {
			index++
			for index < len(text) {
				last := text[index]
				index++
				if last >= '@' && last <= '~' {
					break
				}
			}
		}
	}
	return visible.String()
}
