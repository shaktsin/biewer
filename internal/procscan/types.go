// Package procscan takes periodic snapshots of the OS process table and
// listening TCP ports, without requiring root or a kernel extension.
//
// It is intentionally platform-specific: process listing and port discovery
// have no portable API in Go's standard library. A darwin implementation
// (native mode target, via `ps` and `lsof`) and a linux implementation (via
// /proc, used for local development and testing, and useful in its own
// right for Linux hosts or the future managed-VM guest probe) both satisfy
// the same Scanner interface.
package procscan

import (
	"context"
	"time"
)

// RawProcess is one row of the OS process table, as reported by the
// platform scanner, before any session attribution.
type RawProcess struct {
	PID       int
	PPID      int
	Command   string
	Cwd       string
	StartedAt time.Time
	RSSBytes  uint64
	CPUPct    float64
}

// Snapshot is one point-in-time scan of the process table and listening
// ports.
type Snapshot struct {
	// Processes maps pid -> process. Every currently-running process on the
	// host is included; attribution to sessions happens downstream.
	Processes map[int]RawProcess
	// ListenPorts maps pid -> the TCP ports that pid currently holds in
	// LISTEN state.
	ListenPorts map[int][]int
}

// Scanner takes a snapshot of the current process table and listening
// ports.
type Scanner interface {
	Scan(ctx context.Context) (Snapshot, error)
}

// Children returns pid -> direct child pids, derived from a snapshot's PPID
// links. Useful for walking a process tree from a known root pid.
func (s Snapshot) Children() map[int][]int {
	out := make(map[int][]int, len(s.Processes))
	for pid, p := range s.Processes {
		out[p.PPID] = append(out[p.PPID], pid)
	}
	return out
}

// Descendants returns every pid reachable from root (inclusive) by
// following child links in the snapshot. This is how Biewer attributes
// processes to a session: root is the session's recorded agent PID, and
// anything still linked beneath it in the live tree is "confirmed" evidence
// per the project's confidence model.
func (s Snapshot) Descendants(root int) []int {
	if _, ok := s.Processes[root]; !ok {
		return nil
	}
	children := s.Children()
	var out []int
	queue := []int{root}
	seen := map[int]bool{}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	return out
}
