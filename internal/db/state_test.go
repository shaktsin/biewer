package db

import (
	"testing"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

func TestStateDBRoundTrip(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	snapshot := model.Snapshot{
		GeneratedAt: time.Now(), Storage: state.Name(),
		ProjectUsage: []model.ProjectUsage{{Project: "app", Usage: model.TokenUsage{TotalTokens: 42}}},
	}
	if err := state.PutSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := state.Snapshot()
	if err != nil || len(got.ProjectUsage) != 1 || got.ProjectUsage[0].Usage.TotalTokens != 42 {
		t.Fatalf("snapshot roundtrip: got=%+v err=%v", got, err)
	}

	sources := []model.UsageSource{{Path: "/tmp/a.jsonl", SessionID: "s1", Usage: model.TokenUsage{TotalTokens: 42}}}
	if err := state.PutUsageSources(sources); err != nil {
		t.Fatal(err)
	}
	gotSources, err := state.UsageSources()
	if err != nil || len(gotSources) != 1 || gotSources[0].SessionID != "s1" {
		t.Fatalf("usage roundtrip: got=%+v err=%v", gotSources, err)
	}

	createdAt := time.Now().UTC()
	telemetrySessions := []model.TelemetrySession{{
		SessionID: "thread-1", Agent: model.AgentCodex, LaunchID: "launch-1",
		ProcessPID: 4242, ProcessCreatedAt: createdAt, Usage: model.TokenUsage{TotalTokens: 99},
	}}
	if err := state.PutTelemetrySessions(telemetrySessions); err != nil {
		t.Fatal(err)
	}
	gotTelemetry, err := state.TelemetrySessions()
	if err != nil || len(gotTelemetry) != 1 || gotTelemetry[0].Usage.TotalTokens != 99 || gotTelemetry[0].LaunchID != "launch-1" || gotTelemetry[0].ProcessPID != 4242 || !gotTelemetry[0].ProcessCreatedAt.Equal(createdAt) {
		t.Fatalf("telemetry roundtrip: got=%+v err=%v", gotTelemetry, err)
	}
}
