package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	claudeSessionStartCmd = "biewer hook claude session_start $PPID"
	claudeUserPromptCmd   = "biewer hook claude user_prompt_submit"
	claudePreToolCmd      = "biewer hook claude pre_tool_use"
	claudeSessionEndCmd   = "biewer hook claude session_end"

	// biewerHookCmdPrefix identifies any command Biewer may have installed,
	// so uninstall can remove them (and installs elsewhere aren't touched)
	// even if the exact command string changes across versions.
	biewerHookCmdPrefix = "biewer hook claude"
)

var claudeHookEvents = map[string]string{
	"SessionStart":     claudeSessionStartCmd,
	"UserPromptSubmit": claudeUserPromptCmd,
	"PreToolUse":       claudePreToolCmd,
	"SessionEnd":       claudeSessionEndCmd,
}

const wrapperMarkerBegin = "# >>> biewer codex wrapper >>>"
const wrapperMarkerEnd = "# <<< biewer codex wrapper <<<"

const codexWrapperBlock = wrapperMarkerBegin + `
# Managed by ` + "`biewer hooks install`" + `. Edits here are overwritten by
# ` + "`biewer hooks install`" + ` and removed by ` + "`biewer hooks uninstall`" + `.
codex() {
  if ! command -v biewer >/dev/null 2>&1; then
    command codex "$@"
    return $?
  fi
  local biewer_sid
  biewer_sid=$(biewer hook new-session-id 2>/dev/null)
  if [ -z "$biewer_sid" ]; then
    command codex "$@"
    return $?
  fi
  local biewer_otel_attrs="biewer.launch.id=$biewer_sid"
  if [ -n "${OTEL_RESOURCE_ATTRIBUTES:-}" ]; then
    biewer_otel_attrs="${OTEL_RESOURCE_ATTRIBUTES},${biewer_otel_attrs}"
  fi
  ( OTEL_RESOURCE_ATTRIBUTES="$biewer_otel_attrs" command codex "$@" ) &
  local biewer_pid=$!
  biewer hook codex-start "$PWD" "$biewer_pid" "$biewer_sid" >/dev/null 2>&1
  wait "$biewer_pid"
  local biewer_ec=$?
  [ -n "$biewer_sid" ] && biewer hook codex-end "$biewer_sid" >/dev/null 2>&1
  return $biewer_ec
}
` + wrapperMarkerEnd + "\n"

func cmdHooks(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: biewer hooks <install|uninstall>")
		return 2
	}
	switch args[0] {
	case "install":
		return hooksInstall()
	case "uninstall":
		return hooksUninstall()
	default:
		fmt.Fprintf(os.Stderr, "biewer hooks: unknown subcommand %q\n", args[0])
		return 2
	}
}

func hooksInstall() int {
	claudePath, err := installClaudeHooks()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer hooks install: Claude Code hooks:", err)
		return 1
	}
	rcPath, err := installCodexWrapper()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer hooks install: Codex wrapper:", err)
		return 1
	}
	fmt.Println("Installed Claude and Codex hooks")
	fmt.Printf("  Claude Code: %s (SessionStart, UserPromptSubmit, PreToolUse, SessionEnd)\n", claudePath)
	fmt.Printf("  Codex:       %s (shell wrapper function; restart your shell or 'source %s')\n", rcPath, rcPath)
	fmt.Println("Run 'biewer enable' if you haven't, then use 'claude' or 'codex' normally.")
	return 0
}

func hooksUninstall() int {
	claudePath, err := uninstallClaudeHooks()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer hooks uninstall: Claude Code hooks:", err)
		return 1
	}
	rcPath, changed, err := uninstallCodexWrapper()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer hooks uninstall: Codex wrapper:", err)
		return 1
	}
	fmt.Println("Removed Biewer hooks")
	fmt.Printf("  Claude Code: %s\n", claudePath)
	if changed {
		fmt.Printf("  Codex:       removed wrapper from %s (restart your shell)\n", rcPath)
	} else {
		fmt.Printf("  Codex:       no wrapper found in %s\n", rcPath)
	}
	return 0
}

// --- Claude Code settings.json ------------------------------------------

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func loadJSONObject(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w (fix or remove it, then re-run)", path, err)
	}
	return m, nil
}

func saveJSONObjectAtomic(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func installClaudeHooks() (string, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return "", err
	}
	root, err := loadJSONObject(path)
	if err != nil {
		return "", err
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, command := range claudeHookEvents {
		hooks[event] = addCommandHook(hooks[event], command)
	}
	root["hooks"] = hooks

	if err := saveJSONObjectAtomic(path, root); err != nil {
		return "", err
	}
	return path, nil
}

func uninstallClaudeHooks() (string, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return "", err
	}
	root, err := loadJSONObject(path)
	if err != nil {
		return "", err
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks != nil {
		for event := range claudeHookEvents {
			if v, ok := hooks[event]; ok {
				cleaned := removeCommandHook(v, biewerHookCmdPrefix)
				if isEmptyHookList(cleaned) {
					delete(hooks, event)
				} else {
					hooks[event] = cleaned
				}
			}
		}
		if len(hooks) == 0 {
			delete(root, "hooks")
		} else {
			root["hooks"] = hooks
		}
	}
	if err := saveJSONObjectAtomic(path, root); err != nil {
		return "", err
	}
	return path, nil
}

// addCommandHook appends {"hooks":[{"type":"command","command":command}]}
// to existing (a []any of matcher-groups, per Claude Code's hook config
// shape), unless that exact command is already present anywhere in it.
func addCommandHook(existing any, command string) []any {
	list, _ := existing.([]any)
	for _, group := range list {
		if hasCommand(group, command) {
			return list // idempotent: already installed
		}
	}
	newGroup := map[string]any{
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	}
	return append(list, newGroup)
}

func hasCommand(group any, command string) bool {
	m, ok := group.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if c, _ := hm["command"].(string); c == command {
			return true
		}
	}
	return false
}

// removeCommandHook drops any inner command entries whose command starts
// with prefix, and drops matcher-groups that become empty as a result.
func removeCommandHook(existing any, prefix string) []any {
	list, _ := existing.([]any)
	var out []any
	for _, group := range list {
		m, ok := group.(map[string]any)
		if !ok {
			out = append(out, group)
			continue
		}
		inner, _ := m["hooks"].([]any)
		var keep []any
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				keep = append(keep, h)
				continue
			}
			c, _ := hm["command"].(string)
			if strings.HasPrefix(c, prefix) {
				continue // drop: this is one of ours
			}
			keep = append(keep, h)
		}
		if len(keep) == 0 {
			continue // drop the whole matcher-group
		}
		m["hooks"] = keep
		out = append(out, m)
	}
	return out
}

func isEmptyHookList(list []any) bool { return len(list) == 0 }

// --- Codex shell wrapper --------------------------------------------------

func codexRcPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	shell := os.Getenv("SHELL")
	if strings.Contains(shell, "bash") {
		if _, err := os.Stat(filepath.Join(home, ".bashrc")); err == nil {
			return filepath.Join(home, ".bashrc"), nil
		}
		return filepath.Join(home, ".bash_profile"), nil
	}
	// zsh is the macOS default (and Biewer's primary target) since macOS
	// Catalina; fall back to it whenever $SHELL isn't clearly bash.
	return filepath.Join(home, ".zshrc"), nil
}

func installCodexWrapper() (string, error) {
	path, err := codexRcPath()
	if err != nil {
		return "", err
	}
	existing, err := readFileOrEmpty(path)
	if err != nil {
		return "", err
	}
	stripped := stripMarkedBlock(existing, wrapperMarkerBegin, wrapperMarkerEnd)
	updated := stripped
	if !strings.HasSuffix(updated, "\n") && updated != "" {
		updated += "\n"
	}
	updated += "\n" + codexWrapperBlock
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func uninstallCodexWrapper() (string, bool, error) {
	path, err := codexRcPath()
	if err != nil {
		return "", false, err
	}
	existing, err := readFileOrEmpty(path)
	if err != nil {
		return "", false, err
	}
	stripped := stripMarkedBlock(existing, wrapperMarkerBegin, wrapperMarkerEnd)
	if stripped == existing {
		return path, false, nil
	}
	if err := os.WriteFile(path, []byte(stripped), 0o644); err != nil {
		return "", false, err
	}
	return path, true, nil
}

func readFileOrEmpty(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// stripMarkedBlock removes a single begin/end-marked block (inclusive) from
// content, if present, so re-installing is idempotent (no duplicate
// wrappers piling up across repeated `biewer hooks install` runs).
func stripMarkedBlock(content, begin, end string) string {
	startIdx := strings.Index(content, begin)
	if startIdx < 0 {
		return content
	}
	endIdx := strings.Index(content[startIdx:], end)
	if endIdx < 0 {
		return content
	}
	endIdx = startIdx + endIdx + len(end)
	// Also eat exactly the one leading/trailing newline install() adds
	// around the block, so repeated install/uninstall doesn't accumulate
	// or eat unrelated blank lines the user already had.
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}
	if startIdx > 0 && content[startIdx-1] == '\n' {
		startIdx--
	}
	return content[:startIdx] + content[endIdx:]
}
