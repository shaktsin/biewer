package db

import (
	"context"
	"testing"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

func TestUpsertAndGetSession(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	s := model.Session{
		ID:             "abc123def456",
		Agent:          model.AgentClaude,
		Project:        "biewer",
		Cwd:            "/Users/shaktsin/projects/biewer",
		RootPID:        4242,
		StartedAt:      time.Now().Add(-time.Hour),
		LastActivityAt: time.Now(),
		State:          model.StateActive,
	}
	if err := store.UpsertSession(ctx, s, 1024); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	got, err := store.GetSession(ctx, "abc123def456")
	if err != nil {
		t.Fatalf("GetSession exact: %v", err)
	}
	if got.Project != "biewer" || got.RootPID != 4242 {
		t.Errorf("unexpected session content: %+v", got)
	}
	if got.PeakMemoryBytes != 1024 {
		t.Errorf("expected peak memory 1024, got %d", got.PeakMemoryBytes)
	}

	// Prefix lookup.
	got2, err := store.GetSession(ctx, "abc1")
	if err != nil {
		t.Fatalf("GetSession prefix: %v", err)
	}
	if got2.ID != s.ID {
		t.Errorf("prefix lookup returned wrong session: %+v", got2)
	}

	// Peak memory should only ever increase via upsert.
	if err := store.UpsertSession(ctx, s, 512); err != nil {
		t.Fatalf("UpsertSession lower peak: %v", err)
	}
	got3, _ := store.GetSession(ctx, s.ID)
	if got3.PeakMemoryBytes != 1024 {
		t.Errorf("expected peak memory to stay at 1024 (max), got %d", got3.PeakMemoryBytes)
	}
}

func TestAmbiguousPrefix(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	base := model.Session{Agent: model.AgentCodex, StartedAt: time.Now(), LastActivityAt: time.Now(), State: model.StateActive}
	s1 := base
	s1.ID = "abcd1111"
	s2 := base
	s2.ID = "abcd2222"
	if err := store.UpsertSession(ctx, s1, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSession(ctx, s2, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetSession(ctx, "abcd"); err == nil {
		t.Fatal("expected an ambiguous-prefix error")
	}
}

func TestRecentSessionsOrderingAndLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()
	for i, id := range []string{"s-oldest", "s-middle", "s-newest"} {
		s := model.Session{
			ID: id, Agent: model.AgentClaude, State: model.StateEnded,
			StartedAt: now.Add(time.Duration(i) * time.Minute), LastActivityAt: now,
		}
		if err := store.UpsertSession(ctx, s, 0); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.RecentSessions(ctx, 2)
	if err != nil {
		t.Fatalf("RecentSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected limit=2 to return 2 sessions, got %d", len(got))
	}
	if got[0].ID != "s-newest" || got[1].ID != "s-middle" {
		t.Errorf("expected newest-first ordering, got %s, %s", got[0].ID, got[1].ID)
	}
}

func TestRecordAndReadEvents(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := []model.Event{
		{Kind: model.EventSessionStart, SessionID: "s1", Agent: model.AgentClaude, Timestamp: time.Now()},
		{Kind: model.EventPreToolUse, SessionID: "s1", ToolName: "Bash", Timestamp: time.Now()},
		{Kind: model.EventSessionStart, SessionID: "s2", Agent: model.AgentCodex, Timestamp: time.Now()},
	}
	for _, e := range events {
		if err := store.RecordEvent(ctx, e); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}

	got, err := store.ReadEvents("s1")
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events for session s1, got %d: %+v", len(got), got)
	}
}
