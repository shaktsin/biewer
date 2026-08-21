// Package telemetry decodes the small, privacy-safe subset of OTLP/JSON that
// Biewer needs for logical Codex and Claude Code session attribution. It
// deliberately ignores log bodies, prompts, tool arguments and output.
package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

// Event is one normalized Codex or Claude Code telemetry record. Counters are
// accumulated by the daemon after duplicate delivery has been checked.
type Event struct {
	Agent            model.Agent
	SessionID        string
	LaunchID         string
	ProcessPID       int
	ProcessCreatedAt time.Time
	TurnID           string
	Name             string
	Cwd              string
	Model            string
	Timestamp        time.Time
	Usage            model.TokenUsage
	Metrics          model.AgentMetrics
	Fingerprint      string
}

type anyValue struct {
	StringValue string          `json:"stringValue"`
	IntValue    json.RawMessage `json:"intValue"`
	DoubleValue json.RawMessage `json:"doubleValue"`
	BoolValue   *bool           `json:"boolValue"`
}

type attribute struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type logRecord struct {
	TimeUnixNano         json.RawMessage `json:"timeUnixNano"`
	ObservedTimeUnixNano json.RawMessage `json:"observedTimeUnixNano"`
	Attributes           []attribute     `json:"attributes"`
}

type scopeLogs struct {
	Scope struct {
		Name string `json:"name"`
	} `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}

type resourceLogs struct {
	Resource struct {
		Attributes []attribute `json:"attributes"`
	} `json:"resource"`
	ScopeLogs []scopeLogs `json:"scopeLogs"`
}

type exportLogsRequest struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}

// DecodeLogs decodes an OTLP/HTTP JSON logs request. Only attributes used for
// session identity, model metadata, timestamps and token counters survive.
func DecodeLogs(r io.Reader) ([]Event, error) {
	var request exportLogsRequest
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("decode OTLP logs JSON: %w", err)
	}

	var events []Event
	for _, resource := range request.ResourceLogs {
		resourceAttrs := attributesMap(resource.Resource.Attributes)
		for _, scope := range resource.ScopeLogs {
			for _, record := range scope.LogRecords {
				attrs := cloneMap(resourceAttrs)
				if scope.Scope.Name != "" {
					attrs["otel.scope.name"] = scope.Scope.Name
				}
				for key, value := range attributesMap(record.Attributes) {
					attrs[key] = value
				}
				event := normalize(attrs, record.TimeUnixNano, record.ObservedTimeUnixNano)
				if event.SessionID != "" {
					events = append(events, event)
				}
			}
		}
	}
	return events, nil
}

func normalize(attrs map[string]string, timeUnixNano, observedTimeUnixNano json.RawMessage) Event {
	event := Event{
		SessionID:  first(attrs, "conversation.id", "thread.id", "session.id"),
		LaunchID:   first(attrs, "biewer.launch.id"),
		ProcessPID: firstInt(attrs, "process.pid"),
		TurnID:     first(attrs, "turn.id", "codex.turn.id", "prompt.id"),
		Name:       first(attrs, "event.name", "otel.name"),
		Cwd:        first(attrs, "cwd", "workspace.cwd", "process.cwd"),
		Model:      first(attrs, "model", "gen_ai.request.model", "slug"),
	}
	event.ProcessCreatedAt = parseTimestamp(first(attrs, "process.creation.time", "process.start_time"))
	event.Agent = detectAgent(attrs, event.Name)
	event.Timestamp = parseTimestamp(first(attrs, "event.timestamp"))
	if event.Timestamp.IsZero() {
		event.Timestamp = unixNanoTime(timeUnixNano)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = unixNanoTime(observedTimeUnixNano)
	}

	input := firstUint(attrs,
		"codex.turn.token_usage.input_tokens", "gen_ai.usage.input_tokens", "input_token_count")
	cached := firstUint(attrs,
		"codex.turn.token_usage.cached_input_tokens", "gen_ai.usage.cache_read.input_tokens", "cached_token_count")
	if cached <= input {
		input -= cached
	}
	event.Usage = model.TokenUsage{
		InputTokens:       input,
		CachedInputTokens: cached,
		CacheWriteTokens: firstUint(attrs,
			"codex.turn.token_usage.cache_write_input_tokens", "gen_ai.usage.cache_write.input_tokens", "cache_write_token_count"),
		OutputTokens: firstUint(attrs,
			"codex.turn.token_usage.output_tokens", "gen_ai.usage.output_tokens", "output_token_count"),
		ReasoningTokens: firstUint(attrs,
			"codex.turn.token_usage.reasoning_output_tokens", "codex.usage.reasoning_output_tokens", "reasoning_token_count"),
		TotalTokens: firstUint(attrs,
			"codex.turn.token_usage.total_tokens", "codex.usage.total_tokens", "tool_token_count"),
	}
	if event.Usage.TotalTokens == 0 {
		event.Usage.TotalTokens = event.Usage.InputTokens + event.Usage.CachedInputTokens + event.Usage.CacheWriteTokens + event.Usage.OutputTokens
	}

	setFingerprint(&event, "")
	return event
}

type numberDataPoint struct {
	Attributes   []attribute     `json:"attributes"`
	TimeUnixNano json.RawMessage `json:"timeUnixNano"`
	AsInt        json.RawMessage `json:"asInt"`
	AsDouble     json.RawMessage `json:"asDouble"`
}

type metricSum struct {
	DataPoints []numberDataPoint `json:"dataPoints"`
}

type metricGauge struct {
	DataPoints []numberDataPoint `json:"dataPoints"`
}

type otlpMetric struct {
	Name  string      `json:"name"`
	Sum   metricSum   `json:"sum"`
	Gauge metricGauge `json:"gauge"`
}

type scopeMetrics struct {
	Metrics []otlpMetric `json:"metrics"`
}

type resourceMetrics struct {
	Resource struct {
		Attributes []attribute `json:"attributes"`
	} `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}

type exportMetricsRequest struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}

// DecodeMetrics decodes Claude Code's OTLP/HTTP JSON metric exports. Claude's
// default delta temporality means each datapoint can be safely accumulated;
// duplicate batch delivery is handled by the event fingerprint in the daemon.
func DecodeMetrics(r io.Reader) ([]Event, error) {
	var request exportMetricsRequest
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("decode OTLP metrics JSON: %w", err)
	}

	var events []Event
	for _, resource := range request.ResourceMetrics {
		resourceAttrs := attributesMap(resource.Resource.Attributes)
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				points := append([]numberDataPoint(nil), metric.Sum.DataPoints...)
				points = append(points, metric.Gauge.DataPoints...)
				for _, point := range points {
					attrs := cloneMap(resourceAttrs)
					for key, value := range attributesMap(point.Attributes) {
						attrs[key] = value
					}
					event := normalizeMetric(metric.Name, attrs, point)
					if event.SessionID != "" {
						events = append(events, event)
					}
				}
			}
		}
	}
	return events, nil
}

func normalizeMetric(name string, attrs map[string]string, point numberDataPoint) Event {
	value := numberPointValue(point)
	event := Event{
		Agent:      detectAgent(attrs, name),
		SessionID:  first(attrs, "session.id", "conversation.id", "thread.id"),
		LaunchID:   first(attrs, "biewer.launch.id"),
		ProcessPID: firstInt(attrs, "process.pid"),
		Name:       name,
		TurnID:     first(attrs, "prompt.id"),
		Cwd:        first(attrs, "cwd", "workspace.cwd", "process.cwd"),
		Model:      first(attrs, "model", "gen_ai.request.model"),
		Timestamp:  unixNanoTime(point.TimeUnixNano),
	}
	event.ProcessCreatedAt = parseTimestamp(first(attrs, "process.creation.time", "process.start_time"))
	metricType := first(attrs, "type")
	if event.Agent == model.AgentClaude {
		switch name {
		case "claude_code.token.usage":
			tokens := nonNegativeUint(value)
			switch metricType {
			case "input":
				event.Usage.InputTokens = tokens
			case "output":
				event.Usage.OutputTokens = tokens
			case "cacheRead":
				event.Usage.CachedInputTokens = tokens
			case "cacheCreation":
				event.Usage.CacheWriteTokens = tokens
			}
			event.Usage.TotalTokens = tokens
		case "claude_code.cost.usage":
			event.Metrics.CostUSD = maxFloat(0, value)
		case "claude_code.active_time.total":
			event.Metrics.ActiveSeconds = maxFloat(0, value)
		case "claude_code.lines_of_code.count":
			if metricType == "added" {
				event.Metrics.LinesAdded = nonNegativeUint(value)
			} else if metricType == "removed" {
				event.Metrics.LinesRemoved = nonNegativeUint(value)
			}
		case "claude_code.commit.count":
			event.Metrics.Commits = nonNegativeUint(value)
		case "claude_code.pull_request.count":
			event.Metrics.PullRequests = nonNegativeUint(value)
		}
	}
	setFingerprint(&event, metricType+"\x00"+strconv.FormatFloat(value, 'g', -1, 64))
	return event
}

func setFingerprint(event *Event, discriminator string) {
	fingerprintInput := string(event.Agent) + "\x00" + event.SessionID + "\x00" + event.LaunchID + "\x00" + strconv.Itoa(event.ProcessPID) + "\x00" + event.ProcessCreatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + event.TurnID + "\x00" + event.Name + "\x00" + event.Timestamp.UTC().Format(time.RFC3339Nano) + "\x00" + discriminator + "\x00" + fmt.Sprintf("%+v\x00%+v", event.Usage, event.Metrics)
	sum := sha256.Sum256([]byte(fingerprintInput))
	event.Fingerprint = hex.EncodeToString(sum[:16])
}

func firstInt(attrs map[string]string, keys ...string) int {
	for _, key := range keys {
		value, err := strconv.Atoi(attrs[key])
		if err == nil && value > 0 {
			return value
		}
	}
	return 0
}

func detectAgent(attrs map[string]string, name string) model.Agent {
	identity := strings.ToLower(strings.Join([]string{name, attrs["service.name"], attrs["otel.scope.name"], attrs["gen_ai.system"]}, " "))
	if strings.Contains(identity, "claude") || strings.Contains(identity, "anthropic") {
		return model.AgentClaude
	}
	return model.AgentCodex
}

func numberPointValue(point numberDataPoint) float64 {
	for _, raw := range []json.RawMessage{point.AsDouble, point.AsInt} {
		if len(raw) == 0 {
			continue
		}
		value, err := strconv.ParseFloat(rawScalar(raw), 64)
		if err == nil {
			return value
		}
	}
	return 0
}

func nonNegativeUint(value float64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(math.Round(value))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func attributesMap(attributes []attribute) map[string]string {
	out := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		if attribute.Key == "" {
			continue
		}
		if attribute.Value.StringValue != "" {
			out[attribute.Key] = attribute.Value.StringValue
			continue
		}
		if len(attribute.Value.IntValue) > 0 {
			out[attribute.Key] = rawScalar(attribute.Value.IntValue)
			continue
		}
		if len(attribute.Value.DoubleValue) > 0 {
			out[attribute.Key] = rawScalar(attribute.Value.DoubleValue)
			continue
		}
		if attribute.Value.BoolValue != nil {
			out[attribute.Key] = strconv.FormatBool(*attribute.Value.BoolValue)
		}
	}
	return out
}

func rawScalar(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func unixNanoTime(raw json.RawMessage) time.Time {
	value := rawScalar(raw)
	nanos, err := strconv.ParseInt(value, 10, 64)
	if err != nil || nanos <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

func parseTimestamp(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func first(attrs map[string]string, keys ...string) string {
	for _, key := range keys {
		if attrs[key] != "" {
			return attrs[key]
		}
	}
	return ""
}

func firstUint(attrs map[string]string, keys ...string) uint64 {
	value := first(attrs, keys...)
	parsed, _ := strconv.ParseUint(value, 10, 64)
	return parsed
}

func cloneMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
