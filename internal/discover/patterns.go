// Package discover finds coding-agent and AI-desktop-app processes that
// were never announced by a hook — Claude Desktop, the ChatGPT desktop
// app, or a bare `claude`/`codex` CLI invocation with no hooks installed.
//
// This trades precision for zero setup: there's no session_start event, so
// there's no exact "this is the moment it began" and no per-tool activity
// signal, which is why idle-dev-server findings never fire for an
// auto-discovered session (see daemon.Tick — LastActivityAt is left zero
// on purpose, and findings.Evaluate already treats a zero LastActivityAt as
// "no evidence, don't warn"). What auto-discovery *does* give you for free:
// live CPU/memory/port attribution and the full spawned process tree,
// the moment `biewer enable` runs — no hooks, no wrapper, no waiting.
package discover

import (
	"strings"

	"github.com/shaktsin/biewer/internal/model"
	"github.com/shaktsin/biewer/internal/procscan"
)

// Pattern identifies the root process of one auto-discoverable app by its
// command line. Match should only match the single top-level process of an
// app instance (e.g. the main Electron process) — its helper/child
// processes are picked up automatically by walking the live process tree
// from that root, exactly like hook-tracked sessions.
type Pattern struct {
	Agent model.Agent
	Label string
	Match func(command string) bool
}

// containsMatch matches by substring, for GUI apps whose command line is a
// full path into a known .app bundle (e.g.
// "/Applications/Claude.app/Contents/MacOS/Claude"). Case-insensitive:
// this was built without a real Claude Desktop/ChatGPT/Codex install to
// inspect, and macOS app-bundle executable capitalization isn't something
// worth guessing wrong on.
func containsMatch(agent model.Agent, label, substr string) Pattern {
	substr = strings.ToLower(substr)
	return Pattern{Agent: agent, Label: label, Match: func(c string) bool { return strings.Contains(strings.ToLower(c), substr) }}
}

// exactBaseNameMatch matches by exact executable base name (the first
// whitespace-separated token's final path component, case-insensitive),
// for CLI tools — a substring match would false-positive on any path
// merely containing "claude" (e.g. a project directory named claude-notes).
//
// macOS `ps command` does not quote executable paths containing spaces. A
// helper such as ".../Codex Framework.framework/.../browser_crashpad_handler"
// therefore has a first token ending in "Codex" even though its executable
// is crashpad. Embedded .app processes belong to the desktop application's
// shared process tree and must never be promoted to standalone CLI sessions.
func exactBaseNameMatch(agent model.Agent, label, name string) Pattern {
	name = strings.ToLower(name)
	return Pattern{Agent: agent, Label: label, Match: func(c string) bool {
		fields := strings.Fields(c)
		if len(fields) == 0 || strings.Contains(strings.ToLower(fields[0]), ".app/") {
			return false
		}
		return strings.ToLower(baseName(c)) == name
	}}
}

func baseName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	if idx := strings.LastIndexByte(first, '/'); idx >= 0 {
		first = first[idx+1:]
	}
	return first
}

// DefaultPatterns is Biewer's out-of-the-box auto-discovery list. The exact
// substrings for the desktop apps are best-effort against typical macOS
// app-bundle layouts (<App>.app/Contents/MacOS/<App>) — if a real install
// reports a different path, adjust the pattern here; matching only needs
// to catch each app's single top-level process, since everything beneath
// it in the process tree is attributed by lineage regardless of its own
// process name.
func DefaultPatterns() []Pattern {
	return []Pattern{
		containsMatch(model.AgentClaudeDesktop, "Claude Desktop", "Claude.app/Contents/MacOS/Claude"),
		containsMatch(model.AgentChatGPT, "ChatGPT Desktop", "ChatGPT.app/Contents/MacOS/ChatGPT"),
		// OpenAI's desktop app has shipped under a "Codex.app" bundle
		// (bundle id com.openai.codex) even where its window/menu bar
		// label reads "ChatGPT" — match both bundle names since which one
		// is actually on disk varies by install/version.
		containsMatch(model.AgentCodexDesktop, "Codex Desktop", "Codex.app/Contents/MacOS/Codex"),
		exactBaseNameMatch(model.AgentClaude, "claude (auto-detected, no hooks)", "claude"),
		exactBaseNameMatch(model.AgentCodex, "codex (auto-detected, no hooks)", "codex"),
	}
}

func matchOne(command string, patterns []Pattern) (Pattern, bool) {
	for _, p := range patterns {
		if p.Match(command) {
			return p, true
		}
	}
	return Pattern{}, false
}

// Root is one auto-discovered session root: a process that matched a
// Pattern and has no matched ancestor of its own (so it isn't just a
// helper/child process of an app instance already being tracked as a
// root).
type Root struct {
	PID     int
	Pattern Pattern
	Process procscan.RawProcess
}

// FindRoots scans snap for auto-discoverable app roots, skipping any pid in
// exclude (already attributed to a hook-tracked session — see
// daemon.Tick, which builds exclude from every currently hook-tracked
// session's live process tree, so a `claude` CLI process with hooks
// installed is never double-counted as its own separate auto-session).
func FindRoots(snap procscan.Snapshot, patterns []Pattern, exclude map[int]bool) []Root {
	matched := make(map[int]Pattern, len(snap.Processes))
	for pid, p := range snap.Processes {
		if exclude[pid] {
			continue
		}
		if pat, ok := matchOne(p.Command, patterns); ok {
			matched[pid] = pat
		}
	}

	var roots []Root
	for pid, pat := range matched {
		if hasMatchedAncestor(snap, pid, matched) {
			continue // a helper/child of an app instance whose top-level process is also matched
		}
		roots = append(roots, Root{PID: pid, Pattern: pat, Process: snap.Processes[pid]})
	}
	return roots
}

// hasMatchedAncestor reports whether any ancestor of pid (via live PPID
// links) is itself in matched, guarding against cycles/missing links in a
// malformed snapshot.
func hasMatchedAncestor(snap procscan.Snapshot, pid int, matched map[int]Pattern) bool {
	seen := map[int]bool{pid: true}
	cur := pid
	for i := 0; i < 1000; i++ {
		proc, ok := snap.Processes[cur]
		if !ok {
			return false
		}
		parent := proc.PPID
		if parent == 0 || seen[parent] {
			return false
		}
		if _, ok := matched[parent]; ok {
			return true
		}
		seen[parent] = true
		cur = parent
	}
	return false
}
