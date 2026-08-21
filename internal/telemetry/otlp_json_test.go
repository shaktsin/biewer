package telemetry

import (
	"strings"
	"testing"

	"github.com/shaktsin/biewer/internal/model"
)

func TestDecodeLogsExtractsOnlySessionMetadataAndTokenCounters(t *testing.T) {
	payload := `{
  "resourceLogs": [{
    "resource": {"attributes": [
      {"key":"service.name","value":{"stringValue":"codex_cli_rs"}},
      {"key":"biewer.launch.id","value":{"stringValue":"launch-1"}},
      {"key":"process.pid","value":{"intValue":"4242"}},
      {"key":"process.creation.time","value":{"stringValue":"2026-08-20T20:00:00Z"}}
    ]},
    "scopeLogs": [{"logRecords": [{
      "timeUnixNano":"1787274000000000000",
      "body":{"stringValue":"prompt and tool output must be ignored"},
      "attributes": [
        {"key":"conversation.id","value":{"stringValue":"thread-1"}},
        {"key":"turn.id","value":{"stringValue":"turn-1"}},
        {"key":"event.name","value":{"stringValue":"codex.sse_event"}},
        {"key":"event.kind","value":{"stringValue":"response.completed"}},
        {"key":"model","value":{"stringValue":"gpt-5"}},
        {"key":"cwd","value":{"stringValue":"/work/app"}},
        {"key":"input_token_count","value":{"intValue":"100"}},
        {"key":"cached_token_count","value":{"intValue":"40"}},
        {"key":"output_token_count","value":{"intValue":"10"}},
        {"key":"reasoning_token_count","value":{"intValue":"2"}},
        {"key":"tool_token_count","value":{"intValue":"110"}}
      ]
    }]}]
  }]
}`

	events, err := DecodeLogs(strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one normalized event, got %+v", events)
	}
	event := events[0]
	if event.Agent != model.AgentCodex || event.SessionID != "thread-1" || event.TurnID != "turn-1" || event.Cwd != "/work/app" || event.Model != "gpt-5" {
		t.Fatalf("wrong session metadata: %+v", event)
	}
	if event.LaunchID != "launch-1" || event.ProcessPID != 4242 || event.ProcessCreatedAt.IsZero() {
		t.Fatalf("wrong process correlation metadata: %+v", event)
	}
	if event.Usage.InputTokens != 60 || event.Usage.CachedInputTokens != 40 || event.Usage.OutputTokens != 10 || event.Usage.ReasoningTokens != 2 || event.Usage.TotalTokens != 110 {
		t.Fatalf("wrong token usage: %+v", event.Usage)
	}
	if event.Fingerprint == "" {
		t.Fatal("expected a delivery fingerprint")
	}
}

func TestDecodeMetricsExtractsClaudeUsageAndOperationalCounters(t *testing.T) {
	payload := `{
  "resourceMetrics": [{
    "resource": {"attributes": [
      {"key":"service.name","value":{"stringValue":"claude-code"}},
      {"key":"process.pid","value":{"intValue":"5252"}},
      {"key":"process.creation.time","value":{"stringValue":"2026-08-20T20:00:00Z"}}
    ]},
    "scopeMetrics": [{"metrics": [
      {"name":"claude_code.token.usage","sum":{"dataPoints":[
        {"timeUnixNano":"1787274000000000000","asInt":"120","attributes":[
          {"key":"session.id","value":{"stringValue":"claude-1"}},
          {"key":"type","value":{"stringValue":"input"}},
          {"key":"model","value":{"stringValue":"claude-sonnet-5"}}
        ]},
        {"timeUnixNano":"1787274000000000001","asInt":"80","attributes":[
          {"key":"session.id","value":{"stringValue":"claude-1"}},
          {"key":"type","value":{"stringValue":"cacheRead"}}
        ]}
      ]}},
      {"name":"claude_code.cost.usage","sum":{"dataPoints":[
        {"timeUnixNano":"1787274000000000002","asDouble":0.0125,"attributes":[
          {"key":"session.id","value":{"stringValue":"claude-1"}},
          {"key":"model","value":{"stringValue":"claude-sonnet-5"}}
        ]}
      ]}},
      {"name":"claude_code.active_time.total","sum":{"dataPoints":[
        {"timeUnixNano":"1787274000000000003","asDouble":15.5,"attributes":[
          {"key":"session.id","value":{"stringValue":"claude-1"}},
          {"key":"type","value":{"stringValue":"cli"}}
        ]}
      ]}}
    ]}]
  }]
}`

	events, err := DecodeMetrics(strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("expected four Claude metric datapoints, got %+v", events)
	}
	var usage model.TokenUsage
	var metrics model.AgentMetrics
	for _, event := range events {
		if event.Agent != model.AgentClaude || event.SessionID != "claude-1" || event.Fingerprint == "" {
			t.Fatalf("wrong Claude metric identity: %+v", event)
		}
		if event.ProcessPID != 5252 || event.ProcessCreatedAt.IsZero() {
			t.Fatalf("Claude process identity was not retained: %+v", event)
		}
		usage.InputTokens += event.Usage.InputTokens
		usage.CachedInputTokens += event.Usage.CachedInputTokens
		usage.TotalTokens += event.Usage.TotalTokens
		metrics.CostUSD += event.Metrics.CostUSD
		metrics.ActiveSeconds += event.Metrics.ActiveSeconds
	}
	if usage.InputTokens != 120 || usage.CachedInputTokens != 80 || usage.TotalTokens != 200 {
		t.Fatalf("wrong Claude token metrics: %+v", usage)
	}
	if metrics.CostUSD != 0.0125 || metrics.ActiveSeconds != 15.5 {
		t.Fatalf("wrong Claude operational metrics: %+v", metrics)
	}
}
