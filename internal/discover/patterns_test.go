package discover

import (
	"testing"
	"time"

	"github.com/shaktsin/biewer/internal/model"
	"github.com/shaktsin/biewer/internal/procscan"
)

func TestFindRoots_DesktopAppWithHelpers(t *testing.T) {
	snap := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		1:   {PID: 1, PPID: 0, Command: "/sbin/launchd"},
		100: {PID: 100, PPID: 1, Command: "/Applications/Claude.app/Contents/MacOS/Claude"},
		101: {PID: 101, PPID: 100, Command: "/Applications/Claude.app/Contents/Frameworks/Claude Helper (Renderer).app/Contents/MacOS/Claude Helper (Renderer)"},
		102: {PID: 102, PPID: 101, Command: "/Applications/Claude.app/Contents/Frameworks/Claude Helper (GPU).app/Contents/MacOS/Claude Helper (GPU)"},
		200: {PID: 200, PPID: 1, Command: "/usr/sbin/sshd"}, // unrelated, must not match
	}}

	roots := FindRoots(snap, DefaultPatterns(), nil)
	if len(roots) != 1 {
		t.Fatalf("expected exactly 1 root (the top-level Claude process), got %d: %+v", len(roots), roots)
	}
	if roots[0].PID != 100 {
		t.Errorf("expected root pid 100 (top-level Claude), got %d", roots[0].PID)
	}
	if roots[0].Pattern.Agent != model.AgentClaudeDesktop {
		t.Errorf("expected AgentClaudeDesktop, got %s", roots[0].Pattern.Agent)
	}
}

func TestFindRoots_ChatGPTAndCLITogether(t *testing.T) {
	snap := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		1:   {PID: 1, PPID: 0, Command: "/sbin/launchd"},
		300: {PID: 300, PPID: 1, Command: "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT"},
		301: {PID: 301, PPID: 300, Command: "/Applications/ChatGPT.app/Contents/Frameworks/ChatGPT Helper.app/Contents/MacOS/ChatGPT Helper"},
		400: {PID: 400, PPID: 1, Command: "/Users/me/.local/bin/claude"},
		401: {PID: 401, PPID: 400, Command: "npm run dev"},
		500: {PID: 500, PPID: 1, Command: "/usr/local/bin/codex"},
	}}

	roots := FindRoots(snap, DefaultPatterns(), nil)
	if len(roots) != 3 {
		t.Fatalf("expected 3 roots (ChatGPT, claude, codex), got %d: %+v", len(roots), roots)
	}
	byPID := map[int]Root{}
	for _, r := range roots {
		byPID[r.PID] = r
	}
	if _, ok := byPID[300]; !ok {
		t.Error("expected ChatGPT's top-level pid 300 as a root")
	}
	if _, ok := byPID[301]; ok {
		t.Error("ChatGPT Helper (child of 300) must not be its own separate root")
	}
	if got := byPID[400].Pattern.Agent; got != model.AgentClaude {
		t.Errorf("expected pid 400 to match AgentClaude, got %s", got)
	}
	if got := byPID[500].Pattern.Agent; got != model.AgentCodex {
		t.Errorf("expected pid 500 to match AgentCodex, got %s", got)
	}
}

func TestFindRoots_ExcludesAlreadyHookAttributedPIDs(t *testing.T) {
	snap := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		1:   {PID: 1, PPID: 0, Command: "/sbin/launchd"},
		400: {PID: 400, PPID: 1, Command: "/Users/me/.local/bin/claude"},
	}}

	// Without exclusion, the bare `claude` CLI process is auto-discoverable.
	if roots := FindRoots(snap, DefaultPatterns(), nil); len(roots) != 1 {
		t.Fatalf("expected 1 root without exclusion, got %d", len(roots))
	}

	// A hook already announced this pid as a real tracked session — must
	// not be double-listed as a separate auto-discovered one too.
	exclude := map[int]bool{400: true}
	if roots := FindRoots(snap, DefaultPatterns(), exclude); len(roots) != 0 {
		t.Fatalf("expected 0 roots once pid 400 is excluded (already hook-tracked), got %+v", roots)
	}
}

func TestFindRoots_DoesNotMatchUnrelatedPathContainingSubstring(t *testing.T) {
	snap := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		1: {PID: 1, PPID: 0, Command: "/sbin/launchd"},
		// A project directory that happens to contain "claude" in its path
		// must not false-positive against the CLI's exact-base-name match.
		2: {PID: 2, PPID: 1, Command: "/usr/bin/node /Users/me/projects/claude-notes/server.js"},
	}}
	roots := FindRoots(snap, DefaultPatterns(), nil)
	if len(roots) != 0 {
		t.Fatalf("expected no false-positive match on a path merely containing \"claude\", got %+v", roots)
	}
}

func TestFindRoots_DoesNotPromoteDesktopFrameworkHelpersToCLISessions(t *testing.T) {
	snap := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		1: {PID: 1, PPID: 0, Command: "/sbin/launchd"},
		44118: {
			PID: 44118, PPID: 1,
			Command: "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT",
		},
		44132: {
			PID: 44132, PPID: 1,
			Command: "/Applications/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/150/Helpers/browser_crashpad_handler --monitor-self",
		},
		44134: {
			PID: 44134, PPID: 1,
			Command: "/Applications/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Versions/150/Helpers/browser_crashpad_handler --no-periodic-tasks",
		},
		44200: {
			PID: 44200, PPID: 1,
			Command: "/Applications/Claude.app/Contents/Frameworks/Claude Framework.framework/Helpers/crashpad_handler",
		},
	}}

	roots := FindRoots(snap, DefaultPatterns(), nil)
	if len(roots) != 1 || roots[0].PID != 44118 || roots[0].Pattern.Agent != model.AgentChatGPT {
		t.Fatalf("desktop helpers were promoted to fake CLI sessions: %+v", roots)
	}
}

func TestFindRoots_CycleSafe(t *testing.T) {
	// Malformed/cyclic PPID links (should never happen in a real process
	// table, but hasMatchedAncestor must not infinite-loop on one).
	snap := procscan.Snapshot{Processes: map[int]procscan.RawProcess{
		10: {PID: 10, PPID: 11, Command: "/Users/me/.local/bin/claude"},
		11: {PID: 11, PPID: 10, Command: "/Users/me/.local/bin/claude"},
	}}
	// The only property that matters here is termination: a two-node cycle
	// where both nodes match is a degenerate case (never happens in a real
	// process table, where PPID chains always bottom out at pid 0/1), and
	// hasMatchedAncestor is free to conclude both are "under" the other and
	// exclude both — what it must never do is loop forever.
	done := make(chan []Root, 1)
	go func() { done <- FindRoots(snap, DefaultPatterns(), nil) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("FindRoots did not return — likely an infinite loop on cyclic PPID links")
	}
}
