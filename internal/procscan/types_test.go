package procscan

import "testing"

func TestSnapshot_Descendants(t *testing.T) {
	snap := Snapshot{Processes: map[int]RawProcess{
		1:   {PID: 1, PPID: 0},
		100: {PID: 100, PPID: 1},   // session root
		101: {PID: 101, PPID: 100}, // child shell
		102: {PID: 102, PPID: 101}, // grandchild dev server
		200: {PID: 200, PPID: 1},   // unrelated session root
		201: {PID: 201, PPID: 200},
	}}

	got := map[int]bool{}
	for _, pid := range snap.Descendants(100) {
		got[pid] = true
	}
	for _, want := range []int{100, 101, 102} {
		if !got[want] {
			t.Errorf("expected %d among descendants of 100", want)
		}
	}
	for _, unwanted := range []int{200, 201} {
		if got[unwanted] {
			t.Errorf("pid %d must not be a descendant of 100", unwanted)
		}
	}
}

func TestSnapshot_Descendants_UnknownRoot(t *testing.T) {
	snap := Snapshot{Processes: map[int]RawProcess{1: {PID: 1, PPID: 0}}}
	if got := snap.Descendants(9999); got != nil {
		t.Errorf("expected nil descendants for an unknown root, got %v", got)
	}
}

func TestSnapshot_Descendants_ReparentedProcessIsLost(t *testing.T) {
	// Documents a known limitation: once a process is reparented away from
	// the session root (e.g. double-forked and adopted by PID 1), pure
	// lineage-based attribution loses it. This is the "probable" confidence
	// gap the project README calls out as future work (cwd/port
	// corroboration), not a bug in Descendants itself.
	snap := Snapshot{Processes: map[int]RawProcess{
		1:   {PID: 1, PPID: 0},
		100: {PID: 100, PPID: 1},
		102: {PID: 102, PPID: 1}, // was spawned under 100, now reparented to 1
	}}
	got := map[int]bool{}
	for _, pid := range snap.Descendants(100) {
		got[pid] = true
	}
	if got[102] {
		t.Fatal("reparented pid unexpectedly still tracked via lineage alone")
	}
}
