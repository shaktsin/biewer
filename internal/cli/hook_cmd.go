package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/shaktsin/biewer/internal/daemon"
	"github.com/shaktsin/biewer/internal/model"
)

// hookTimeout bounds every hook->daemon round trip. Hooks run inline in the
// user's agent session (Claude Code hooks can block tool execution on their
// exit code; the shell wrapper blocks the prompt returning); Biewer must
// never make that noticeably slower, and must never fail the user's actual
// work just because the daemon isn't running.
const hookTimeout = 1500 * time.Millisecond

// cmdHook implements the internal `biewer hook ...` subcommands invoked by
// installed Claude Code hooks and the Codex shell wrapper. These always
// exit 0: a tracking failure must never block or visibly disrupt the
// agent session it's trying to observe. Problems go to a log file, not
// stdout/stderr.
func cmdHook(args []string) int {
	if len(args) == 0 {
		return 0
	}
	defer func() {
		if r := recover(); r != nil {
			logHookError(fmt.Sprintf("panic: %v", r))
		}
	}()

	switch args[0] {
	case "new-session-id":
		fmt.Println(newSessionID())
	case "claude":
		hookClaude(args[1:])
	case "codex-start":
		hookCodexStart(args[1:])
	case "codex-end":
		hookCodexEnd(args[1:])
	default:
		logHookError(fmt.Sprintf("unknown hook subcommand %q", args[0]))
	}
	return 0
}

type claudeHookPayload struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	ToolName       string `json:"tool_name"`
	TranscriptPath string `json:"transcript_path"`
	Model          string `json:"model"`
}

func hookClaude(args []string) {
	if len(args) == 0 {
		logHookError("hook claude: missing event name")
		return
	}
	event := args[0]

	var payload claudeHookPayload
	body, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		logHookError(fmt.Sprintf("hook claude %s: read stdin: %v", event, err))
		return
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			logHookError(fmt.Sprintf("hook claude %s: parse stdin JSON: %v", event, err))
			return
		}
	}
	if payload.SessionID == "" {
		logHookError(fmt.Sprintf("hook claude %s: no session_id in payload", event))
		return
	}

	e := model.Event{
		SessionID:      payload.SessionID,
		Agent:          model.AgentClaude,
		Cwd:            payload.Cwd,
		ToolName:       payload.ToolName,
		TranscriptPath: payload.TranscriptPath,
		Model:          payload.Model,
		Timestamp:      time.Now(),
	}
	switch event {
	case "session_start":
		e.Kind = model.EventSessionStart
		if len(args) > 1 {
			if pid, err := strconv.Atoi(args[1]); err == nil {
				e.PID = pid
			}
		}
	case "user_prompt_submit":
		e.Kind = model.EventUserPromptSubmit
	case "pre_tool_use":
		e.Kind = model.EventPreToolUse
	case "post_tool_use":
		e.Kind = model.EventPostToolUse
	case "session_end":
		e.Kind = model.EventSessionEnd
	default:
		logHookError(fmt.Sprintf("hook claude: unknown event %q", event))
		return
	}

	postEvent(e)
}

func hookCodexStart(args []string) {
	if len(args) != 2 && len(args) != 3 {
		logHookError("hook codex-start: usage: biewer hook codex-start <cwd> <pid> [launch-id]")
		return
	}
	cwd := args[0]
	pid, err := strconv.Atoi(args[1])
	if err != nil {
		logHookError(fmt.Sprintf("hook codex-start: bad pid %q: %v", args[1], err))
		return
	}

	sid := newSessionID()
	if len(args) == 3 && args[2] != "" {
		sid = args[2]
	}
	// Print the session id first: the wrapper's `sid=$(...)` capture must
	// see it even if the subsequent daemon call is slow or fails.
	fmt.Println(sid)

	postEvent(model.Event{
		Kind: model.EventSessionStart, SessionID: sid, Agent: model.AgentCodex,
		Cwd: cwd, PID: pid, LaunchID: sid, Timestamp: time.Now(),
	})
}

func hookCodexEnd(args []string) {
	if len(args) != 1 {
		logHookError("hook codex-end: usage: biewer hook codex-end <session-id>")
		return
	}
	postEvent(model.Event{Kind: model.EventSessionEnd, SessionID: args[0], Agent: model.AgentCodex, Timestamp: time.Now()})
}

func postEvent(e model.Event) {
	dir, err := biewerDir()
	if err != nil {
		logHookError(fmt.Sprintf("post event: %v", err))
		return
	}
	client := daemon.NewClient(dir)
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	if err := client.PostEvent(ctx, e); err != nil {
		// Very common and expected: daemon not running (`biewer enable`
		// never called, or stopped). Log quietly, don't alarm the user.
		logHookError(fmt.Sprintf("post event %s/%s: %v", e.Kind, shortID(e.SessionID), err))
	}
}

func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to a timestamp so tracking still
		// degrades gracefully instead of crashing the wrapper.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func logHookError(msg string) {
	dir, err := biewerDir()
	if err != nil {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "hook-errors.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format(time.RFC3339), msg)
}
