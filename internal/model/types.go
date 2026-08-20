// Package model defines the core data types shared across Biewer's daemon,
// CLI, and storage layers.
package model

import "time"

// Agent identifies which coding agent a session belongs to.
type Agent string

const (
	AgentClaude  Agent = "claude"
	AgentCodex   Agent = "codex"
	AgentUnknown Agent = "unknown"
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
	ID        string       `json:"id"`
	Agent     Agent        `json:"agent"`
	Project   string       `json:"project"` // best-effort project name (basename of cwd)
	Cwd       string       `json:"cwd"`
	RootPID   int          `json:"root_pid"`
	StartedAt time.Time    `json:"started_at"`
	EndedAt   *time.Time   `json:"ended_at,omitempty"`
	State     SessionState `json:"state"`

	// LastActivityAt is updated by hook events (UserPromptSubmit,
	// PreToolUse, PostToolUse). It is the primary signal for idle detection,
	// since a process can be running (e.g. a dev server) long after the
	// agent itself has gone quiet.
	LastActivityAt time.Time `json:"last_activity_at"`
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
	Kind      EventKind `json:"kind"`
	SessionID string    `json:"session_id"`
	Agent     Agent     `json:"agent"`
	Cwd       string    `json:"cwd,omitempty"`
	PID       int       `json:"pid,omitempty"` // pid of the agent process, set on session_start
	ToolName  string    `json:"tool_name,omitempty"`
	Timestamp time.Time `json:"timestamp"`
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
	PID      int        `json:"pid"`
	PPID     int        `json:"ppid"`
	Command  string     `json:"command"`
	RSSBytes uint64     `json:"rss_bytes"`
	CPUPct   float64    `json:"cpu_pct"`
	Cwd      string     `json:"cwd,omitempty"`
	Ports    []int      `json:"ports,omitempty"`
	Depth    int        `json:"depth"` // depth in the attributed process tree, root = 0
	Children []*Process `json:"children,omitempty"`
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
	Session         Session    `json:"session"`
	ProcessTree     []*Process `json:"process_tree"` // top-level attributed processes (root first)
	ProcessCount    int        `json:"process_count"`
	CPUSeconds      float64    `json:"cpu_seconds"` // instantaneous sum of %CPU across attributed processes
	MemoryBytes     uint64     `json:"memory_bytes"`
	PeakMemoryBytes uint64     `json:"peak_memory_bytes"`
	ListenPorts     []int      `json:"listen_ports"`
	Findings        []Finding  `json:"findings"`
}

// Snapshot is the full payload returned by the daemon for `biewer watch` /
// `biewer status`.
type Snapshot struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Sessions    []SessionSnapshot `json:"sessions"`
}
