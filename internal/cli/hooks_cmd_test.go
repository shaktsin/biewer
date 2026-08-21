package cli

import (
	"strings"
	"testing"
)

func TestAddCommandHook_Idempotent(t *testing.T) {
	var existing any
	first := addCommandHook(existing, "biewer hook claude pre_tool_use")
	if len(first) != 1 {
		t.Fatalf("expected 1 group after first add, got %d", len(first))
	}
	second := addCommandHook(any(first), "biewer hook claude pre_tool_use")
	if len(second) != 1 {
		t.Fatalf("expected addCommandHook to be idempotent, got %d groups: %+v", len(second), second)
	}
}

func TestAddCommandHook_PreservesUnrelatedExistingHooks(t *testing.T) {
	existing := []any{
		map[string]any{
			"matcher": "Bash",
			"hooks": []any{
				map[string]any{"type": "command", "command": "some-other-tool --check"},
			},
		},
	}
	got := addCommandHook(any(existing), "biewer hook claude pre_tool_use")
	if len(got) != 2 {
		t.Fatalf("expected the pre-existing hook group to be preserved alongside ours, got %d groups", len(got))
	}
	if !hasCommand(got[0], "some-other-tool --check") {
		t.Errorf("expected the user's existing hook to survive untouched: %+v", got[0])
	}
	if !hasCommand(got[1], "biewer hook claude pre_tool_use") {
		t.Errorf("expected our new hook to be appended: %+v", got[1])
	}
}

func TestRemoveCommandHook_OnlyRemovesOurs(t *testing.T) {
	existing := []any{
		map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": "some-other-tool --check"},
				map[string]any{"type": "command", "command": "biewer hook claude pre_tool_use"},
			},
		},
	}
	got := removeCommandHook(any(existing), biewerHookCmdPrefix)
	if len(got) != 1 {
		t.Fatalf("expected 1 remaining group, got %d", len(got))
	}
	if !hasCommand(got[0], "some-other-tool --check") {
		t.Errorf("expected the user's hook to survive removal: %+v", got[0])
	}
	if hasCommand(got[0], "biewer hook claude pre_tool_use") {
		t.Errorf("expected biewer's hook to be removed: %+v", got[0])
	}
}

func TestRemoveCommandHook_DropsEmptyGroups(t *testing.T) {
	existing := []any{
		map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": "biewer hook claude session_end"},
			},
		},
	}
	got := removeCommandHook(any(existing), biewerHookCmdPrefix)
	if len(got) != 0 {
		t.Fatalf("expected the now-empty matcher-group to be dropped entirely, got %+v", got)
	}
}

func TestStripMarkedBlock(t *testing.T) {
	content := "export PATH=/usr/bin\n\n" + wrapperMarkerBegin + "\nsome installed content\n" + wrapperMarkerEnd + "\nalias ll='ls -la'\n"
	got := stripMarkedBlock(content, wrapperMarkerBegin, wrapperMarkerEnd)
	want := "export PATH=/usr/bin\nalias ll='ls -la'\n"
	if got != want {
		t.Errorf("stripMarkedBlock mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestStripMarkedBlock_AbsentIsNoop(t *testing.T) {
	content := "export PATH=/usr/bin\n"
	if got := stripMarkedBlock(content, wrapperMarkerBegin, wrapperMarkerEnd); got != content {
		t.Errorf("expected no-op when the block is absent, got %q", got)
	}
}

func TestInstallCodexWrapper_IsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/zsh")

	path1, err := installCodexWrapper()
	if err != nil {
		t.Fatalf("install 1: %v", err)
	}
	path2, err := installCodexWrapper()
	if err != nil {
		t.Fatalf("install 2: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("expected the same rc file across installs, got %s and %s", path1, path2)
	}
	content, err := readFileOrEmpty(path1)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for i := 0; i+len(wrapperMarkerBegin) <= len(content); i++ {
		if content[i:i+len(wrapperMarkerBegin)] == wrapperMarkerBegin {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 wrapper block after two installs, found %d in:\n%s", count, content)
	}
	for _, wanted := range []string{"biewer hook new-session-id", "biewer.launch.id=", "OTEL_RESOURCE_ATTRIBUTES", "codex-start \"$PWD\" \"$biewer_pid\" \"$biewer_sid\""} {
		if !strings.Contains(content, wanted) {
			t.Errorf("installed Codex wrapper missing correlation fragment %q:\n%s", wanted, content)
		}
	}
}
