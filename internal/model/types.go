// Package model defines the core data types shared across Biewer's daemon,
// CLI, and storage layers.
package model

import "time"

// Agent identifies which coding agent a session belongs to.
type Agent string

const (
	AgentClaude        Agent = "claude"
	AgentCodex         Agent = "codex"
	AgentClaudeDesktop Agent = "claude-desktop"
	AgentChatGPT       Agent = "chatgpt"
	AgentCodexDesktop  Agent = "codex-desktop"
	AgentUnknown       Agent = "unknown"
)

// SessionSource distinguishes how Biewer learned about a session.
type SessionSource string

const (
	// SourceHook means a hook (Claude Code) or the shell wrapper (Codex)
	// explicitly announced this session's start/end and activity — the
	// full-precision path, with real idle detection.
	SourceHook SessionSource = "hook"
	// SourceAuto means Biewer found this session itself by pattern-matching
	// a running process (see internal/discover) — zero setup, but no
	// session-start timestamp precision and no per-tool activity signal,
	// so idle-based findings never fire for it (see findings.Evaluate).
	SourceAuto SessionSource = "auto"
	// SourceTranscript means Biewer discovered the logical agent session from
	// the provider's local JSONL transcript. It has exact project and token
	// attribution, but no trustworthy process ownership unless it is merged
	// with a hook session carrying the same provider session ID.
	SourceTranscript SessionSource = "transcript"
	// SourceTelemetry means the logical session was announced through the
	// local OTLP receiver. Transcript data takes precedence when both sources
	// describe the same provider session because it contains cumulative usage
	// and the canonical working directory.
	SourceTelemetry SessionSource = "telemetry"
)

// ResourceScope describes whether a row's process resources can be assigned
// to one logical agent session. Desktop applications multiplex many tasks in
// one process tree, so their totals must remain shared instead of being copied
// onto every transcript-backed task.
type ResourceScope string

const (
	ResourceOwned  ResourceScope = "owned"
	ResourceShared ResourceScope = "shared"
	ResourceNone   ResourceScope = "none"
)

// AttributionConfidence states how strongly Biewer can connect a logical
// agent session to the process resources displayed on its row.
type AttributionConfidence string

const (
	AttributionConfirmed AttributionConfidence = "confirmed"
	AttributionProbable  AttributionConfidence = "probable"
	AttributionShared    AttributionConfidence = "shared"
	AttributionNone      AttributionConfidence = "none"
)

// SessionState is the lifecycle state of a tracked agent session.
type SessionState string

const (
	StateActive SessionState = "active"
	StateEnded  SessionState = "ended"
)

// Session represents one observed invocation of a coding agent (one `claude`
// or `codex` run), from launch to exit.
type Session struct {
	ID               string       `json:"id"`
	LaunchID         string       `json:"launch_id,omitempty"`
	Agent            Agent        `json:"agent"`
	Project          string       `json:"project"` // best-effort project name (basename of cwd)
	Cwd              string       `json:"cwd"`
	RootPID          int          `json:"root_pid"`
	ProcessStartedAt time.Time    `json:"process_started_at,omitempty"`
	StartedAt        time.Time    `json:"started_at"`
	EndedAt          *time.Time   `json:"ended_at,omitempty"`
	State            SessionState `json:"state"`
	// Source is "hook" or "auto" — see SessionSource.
	Source SessionSource `json:"source"`

	// LastActivityAt is updated by hook events (UserPromptSubmit,
	// PreToolUse, PostToolUse). It is the primary signal for idle detection,
	// since a process can be running (e.g. a dev server) long after the
	// agent itself has gone quiet.
	LastActivityAt time.Time `json:"last_activity_at"`
	// TranscriptPath points at the agent's local JSONL session log when the
	// agent exposes one. Biewer reads token counters from it in the daemon;
	// transcript content is never copied into Biewer's database.
	TranscriptPath string `json:"transcript_path,omitempty"`
	Model          string `json:"model,omitempty"`
}

// EventKind enumerates the hook / lifecycle events Biewer understands.
type EventKind string

const (
	EventSessionStart     EventKind = "session_start"
	EventSessionEnd       EventKind = "session_end"
	EventUserPromptSubmit EventKind = "user_prompt_submit"
	EventPreToolUse       EventKind = "pre_tool_use"
	EventPostToolUse      EventKind = "post_tool_use"
)

// Event is a single hook notification forwarded from an agent (via the
// shell wrapper or a native hook command) to the daemon.
type Event struct {
	Kind           EventKind `json:"kind"`
	SessionID      string    `json:"session_id"`
	Agent          Agent     `json:"agent"`
	Cwd            string    `json:"cwd,omitempty"`
	PID            int       `json:"pid,omitempty"` // pid of the agent process, set on session_start
	LaunchID       string    `json:"launch_id,omitempty"`
	ToolName       string    `json:"tool_name,omitempty"`
	TranscriptPath string    `json:"transcript_path,omitempty"`
	Model          string    `json:"model,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

// TokenUsage is an agent-reported token breakdown. TotalTokens follows the
// agent's own total when one is available; the component fields are retained
// separately because cache/reasoning semantics differ between providers.
type TokenUsage struct {
	InputTokens       uint64 `json:"input_tokens"`
	CachedInputTokens uint64 `json:"cached_input_tokens"`
	CacheWriteTokens  uint64 `json:"cache_write_tokens"`
	OutputTokens      uint64 `json:"output_tokens"`
	ReasoningTokens   uint64 `json:"reasoning_tokens"`
	TotalTokens       uint64 `json:"total_tokens"`
}

// AgentMetrics contains optional provider-reported operational counters. The
// fields are intentionally aggregate-only and contain no prompt, response, or
// tool content.
type AgentMetrics struct {
	CostUSD       float64 `json:"cost_usd,omitempty"`
	ActiveSeconds float64 `json:"active_seconds,omitempty"`
	LinesAdded    uint64  `json:"lines_added,omitempty"`
	LinesRemoved  uint64  `json:"lines_removed,omitempty"`
	Commits       uint64  `json:"commits,omitempty"`
	PullRequests  uint64  `json:"pull_requests,omitempty"`
}

// ProjectUsage aggregates token consumption for every locally-observed agent
// transcript whose cwd belongs to the same project.
type ProjectUsage struct {
	Project   string     `json:"project"`
	Cwd       string     `json:"cwd"`
	Usage     TokenUsage `json:"usage"`
	Sessions  int        `json:"sessions"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// UsageSource is the persisted index entry for one agent transcript. It lets
// the background collector skip unchanged JSONL files after daemon restarts.
type UsageSource struct {
	Path             string     `json:"path"`
	Agent            Agent      `json:"agent"`
	SessionID        string     `json:"session_id"`
	Project          string     `json:"project"`
	Cwd              string     `json:"cwd"`
	Usage            TokenUsage `json:"usage"`
	StartedAt        time.Time  `json:"started_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at,omitempty"`
	Model            string     `json:"model,omitempty"`
	Archived         bool       `json:"archived,omitempty"`
	Size             int64      `json:"size"`
	ModifiedUnixNano int64      `json:"modified_unix_nano"`
}

// Confidence describes how sure Biewer is that a process belongs to a
// session, per the project's evidence-hierarchy model.
type Confidence string

const (
	ConfidenceConfirmed Confidence = "confirmed" // currently in the recorded process tree
	ConfidenceProbable  Confidence = "probable"  // lineage broken (e.g. reparented) but cwd/port evidence matches
	ConfidenceUnknown   Confidence = "unknown"
)

// Process is one observed OS process, optionally attributed to a session.
type Process struct {
	PID       int        `json:"pid"`
	PPID      int        `json:"ppid"`
	Command   string     `json:"command"`
	StartedAt time.Time  `json:"started_at,omitempty"`
	RSSBytes  uint64     `json:"rss_bytes"`
	CPUPct    float64    `json:"cpu_pct"`
	Cwd       string     `json:"cwd,omitempty"`
	Ports     []int      `json:"ports,omitempty"`
	Depth     int        `json:"depth"` // depth in the attributed process tree, root = 0
	Children  []*Process `json:"children,omitempty"`
}

// Severity of a Finding.
type Severity string

const (
	SeverityInfo Severity = "INFO"
	SeverityWarn Severity = "WARN"
)

// Finding is a diagnosis about a session, e.g. "dev server left running".
type Finding struct {
	Severity    Severity   `json:"severity"`
	Message     string     `json:"message"`
	Attribution string     `json:"attribution"`
	Confidence  Confidence `json:"confidence"`
}

// SessionSnapshot bundles a session with its currently-attributed process
// tree, resource totals, and findings — everything `biewer watch` needs to
// render one row (plus its expandable tree) for a session.
type SessionSnapshot struct {
	Session         Session               `json:"session"`
	ProcessTree     []*Process            `json:"process_tree"` // top-level attributed processes (root first)
	ProcessCount    int                   `json:"process_count"`
	CPUSeconds      float64               `json:"cpu_seconds"` // instantaneous sum of %CPU across attributed processes
	MemoryBytes     uint64                `json:"memory_bytes"`
	PeakMemoryBytes uint64                `json:"peak_memory_bytes"`
	ListenPorts     []int                 `json:"listen_ports"`
	Findings        []Finding             `json:"findings"`
	Usage           TokenUsage            `json:"usage,omitempty"`
	UsageUpdatedAt  time.Time             `json:"usage_updated_at,omitempty"`
	Metrics         AgentMetrics          `json:"metrics,omitempty"`
	ResourceScope   ResourceScope         `json:"resource_scope,omitempty"`
	Attribution     AttributionConfidence `json:"attribution,omitempty"`
}

// TelemetrySession is the cumulative, privacy-minimized state accepted from
// Codex and Claude Code OTLP exporters. Biewer stores identifiers, model/cwd
// metadata and aggregate counters only; prompts, responses and tool output are
// deliberately ignored.
type TelemetrySession struct {
	SessionID          string       `json:"session_id"`
	Agent              Agent        `json:"agent"`
	Project            string       `json:"project,omitempty"`
	Cwd                string       `json:"cwd,omitempty"`
	Model              string       `json:"model,omitempty"`
	LaunchID           string       `json:"launch_id,omitempty"`
	ProcessPID         int          `json:"process_pid,omitempty"`
	ProcessCreatedAt   time.Time    `json:"process_created_at,omitempty"`
	Usage              TokenUsage   `json:"usage"`
	Metrics            AgentMetrics `json:"metrics,omitempty"`
	StartedAt          time.Time    `json:"started_at,omitempty"`
	UpdatedAt          time.Time    `json:"updated_at,omitempty"`
	LastFingerprint    string       `json:"last_fingerprint,omitempty"`
	RecentFingerprints []string     `json:"recent_fingerprints,omitempty"`
}

// Snapshot is the full payload returned by the daemon for `biewer watch` /
// `biewer status`.
type Snapshot struct {
	GeneratedAt  time.Time         `json:"generated_at"`
	Sessions     []SessionSnapshot `json:"sessions"`
	ProjectUsage []ProjectUsage    `json:"project_usage,omitempty"`
	Storage      string            `json:"storage,omitempty"`
}
