//go:build darwin

package procscan

import "testing"

func TestParseDarwinProcessLineIncludesCreationTimeAndSpacedCommand(t *testing.T) {
	line := `44132 1 7056 1.5 Thu Aug 20 21:01:09 2026 /Applications/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Helpers/crashpad --monitor`
	process, ok := parseDarwinProcessLine(line)
	if !ok {
		t.Fatal("expected process line to parse")
	}
	if process.PID != 44132 || process.PPID != 1 || process.StartedAt.IsZero() {
		t.Fatalf("wrong process identity: %+v", process)
	}
	if process.Command != "/Applications/ChatGPT.app/Contents/Frameworks/Codex Framework.framework/Helpers/crashpad --monitor" {
		t.Fatalf("spaced command was not preserved: %q", process.Command)
	}
}
