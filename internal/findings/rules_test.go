package findings

import (
	"testing"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

func TestEvaluate_IdleDevServerWarns(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	session := model.Session{
		ID:             "sess1",
		LastActivityAt: now.Add(-18 * time.Minute),
	}
	tree := []*model.Process{
		{PID: 1, Command: "codex", Children: []*model.Process{
			{PID: 2, Command: "vite --host 0.0.0.0", Ports: []int{5173}},
		}},
	}

	got := Evaluate(session, tree, now, DefaultOptions())

	var warned bool
	for _, f := range got {
		if f.Severity == model.SeverityWarn {
			warned = true
			if f.Confidence != model.ConfidenceProbable {
				t.Errorf("expected probable confidence, got %s", f.Confidence)
			}
		}
	}
	if !warned {
		t.Fatalf("expected a WARN finding for an idle dev server, got %+v", got)
	}
}

func TestEvaluate_RecentActivityDoesNotWarn(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	session := model.Session{
		LastActivityAt: now.Add(-2 * time.Minute), // well under the 10m default threshold
	}
	tree := []*model.Process{
		{PID: 2, Command: "vite", Ports: []int{5173}},
	}

	got := Evaluate(session, tree, now, DefaultOptions())
	for _, f := range got {
		if f.Severity == model.SeverityWarn {
			t.Fatalf("did not expect a WARN finding for a recently-active session, got %+v", got)
		}
	}
}

func TestEvaluate_NonDevServerPortDoesNotWarn(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	session := model.Session{
		LastActivityAt: now.Add(-1 * time.Hour),
	}
	tree := []*model.Process{
		{PID: 2, Command: "postgres -D /usr/local/var/postgres", Ports: []int{5432}},
	}

	got := Evaluate(session, tree, now, DefaultOptions())
	for _, f := range got {
		if f.Severity == model.SeverityWarn {
			t.Fatalf("did not expect postgres (not a dev-server pattern) to trigger a WARN, got %+v", got)
		}
	}
}

func TestEvaluate_HighMemoryInfo(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	session := model.Session{LastActivityAt: now}
	tree := []*model.Process{
		{PID: 1, Command: "node", RSSBytes: 3 * 1024 * 1024 * 1024},
	}

	got := Evaluate(session, tree, now, DefaultOptions())
	var infoed bool
	for _, f := range got {
		if f.Severity == model.SeverityInfo {
			infoed = true
		}
	}
	if !infoed {
		t.Fatalf("expected an INFO finding for 3 GiB attributed memory, got %+v", got)
	}
}

func TestEvaluate_ZeroLastActivityIsIgnored(t *testing.T) {
	// A session with no recorded activity yet (e.g. hooks not installed,
	// or the very first tick before session_start heartbeat lands) should
	// never be flagged idle — we have no evidence either way.
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	session := model.Session{}
	tree := []*model.Process{{PID: 2, Command: "vite", Ports: []int{5173}}}

	got := Evaluate(session, tree, now, DefaultOptions())
	for _, f := range got {
		if f.Severity == model.SeverityWarn {
			t.Fatalf("did not expect a WARN finding with no LastActivityAt evidence, got %+v", got)
		}
	}
}

func TestFlatten(t *testing.T) {
	tree := []*model.Process{
		{PID: 1, Children: []*model.Process{
			{PID: 2, Children: []*model.Process{{PID: 3}}},
			{PID: 4},
		}},
	}
	got := Flatten(tree)
	if len(got) != 4 {
		t.Fatalf("expected 4 flattened processes, got %d", len(got))
	}
	seen := map[int]bool{}
	for _, p := range got {
		seen[p.PID] = true
	}
	for _, want := range []int{1, 2, 3, 4} {
		if !seen[want] {
			t.Errorf("expected pid %d in flattened output", want)
		}
	}
}
