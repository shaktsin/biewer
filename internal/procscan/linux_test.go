//go:build linux

package procscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeProc builds a minimal fake /proc/<pid>/{stat,cmdline,status} tree
// so the parsing logic can be tested without touching the real process
// table.
func writeFakeProc(t *testing.T, root string, pid, ppid int, comm, cmdline string, rssKB int) {
	t.Helper()
	dir := filepath.Join(root, itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Minimal /proc/<pid>/stat: "pid (comm) state ppid ...zeros"
	stat := itoa(pid) + " (" + comm + ") S " + itoa(ppid) + " 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if cmdline != "" {
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	status := "VmRSS:\t   " + itoa(rssKB) + " kB\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func TestLinuxScanner_ProcessTree(t *testing.T) {
	root := t.TempDir()
	// A pretend Claude Code session: root agent process (pid 100), spawning
	// a shell (200) running `npm run dev` (300).
	writeFakeProc(t, root, 100, 1, "claude", "claude\x00", 50_000)
	writeFakeProc(t, root, 200, 100, "zsh", "zsh\x00", 5_000)
	writeFakeProc(t, root, 300, 200, "node", "npm\x00run\x00dev\x00", 120_000)
	// An unrelated process that must NOT be attributed.
	writeFakeProc(t, root, 999, 1, "sshd", "sshd\x00", 3_000)

	scanner := LinuxScanner{ProcRoot: root}
	snap, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(snap.Processes) != 4 {
		t.Fatalf("expected 4 processes, got %d: %+v", len(snap.Processes), snap.Processes)
	}

	node, ok := snap.Processes[300]
	if !ok {
		t.Fatal("expected pid 300 present")
	}
	if node.Command != "npm run dev" {
		t.Errorf("expected full cmdline %q, got %q", "npm run dev", node.Command)
	}
	if node.RSSBytes != 120_000*1024 {
		t.Errorf("expected RSS %d bytes, got %d", 120_000*1024, node.RSSBytes)
	}
	if node.PPID != 200 {
		t.Errorf("expected ppid 200, got %d", node.PPID)
	}

	descendants := snap.Descendants(100)
	got := map[int]bool{}
	for _, pid := range descendants {
		got[pid] = true
	}
	for _, want := range []int{100, 200, 300} {
		if !got[want] {
			t.Errorf("expected pid %d to be a descendant of 100, descendants=%v", want, descendants)
		}
	}
	if got[999] {
		t.Errorf("unrelated pid 999 must not be attributed as a descendant of 100")
	}
}

func TestLinuxScanner_MissingProcRoot(t *testing.T) {
	scanner := LinuxScanner{ProcRoot: filepath.Join(t.TempDir(), "does-not-exist")}
	if _, err := scanner.Scan(context.Background()); err == nil {
		t.Fatal("expected an error scanning a missing proc root")
	}
}

func TestLinuxScannerReadsProcessCreationTime(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "stat"), []byte("btime 1000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeProc(t, root, 123, 1, "codex", "codex\x00", 100)
	stat := "123 (codex) S 1 " + strings.Repeat("0 ", 17) + "250 0 0\n"
	if err := os.WriteFile(filepath.Join(root, "123", "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}

	scanner := LinuxScanner{ProcRoot: root, ClockTicks: 100}
	snapshot, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := time.Unix(1000, 0).Add(2500 * time.Millisecond)
	if got := snapshot.Processes[123].StartedAt; !got.Equal(want) {
		t.Fatalf("process creation time = %v, want %v", got, want)
	}
}
