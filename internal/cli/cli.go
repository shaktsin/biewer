// Package cli implements the `biewer` command-line interface: everything
// a user or a hook script invokes. It is intentionally dependency-free
// (Go stdlib only) — Biewer's own footprint on a machine full of running
// agents should stay small and boring.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// version is replaced by release builds with -ldflags. Keeping it a variable
// lets local/source builds remain useful without requiring generated files.
var version = "0.1.0-mvp"

// Run dispatches args (os.Args[1:]) to a subcommand and returns the process
// exit code.
func Run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return 1
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "enable":
		return cmdEnable(rest)
	case "disable":
		return cmdDisable(rest)
	case "setup":
		return cmdSetup(rest)
	case "daemon-run": // internal: spawned by `enable`, not for direct use
		return cmdDaemonRun(rest)
	case "status":
		return cmdStatus(rest)
	case "watch":
		return cmdWatch(rest)
	case "tui":
		return cmdTUI(rest)
	case "hooks":
		return cmdHooks(rest)
	case "telemetry":
		return cmdTelemetry(rest)
	case "hook": // internal: invoked by installed hooks/wrappers
		return cmdHook(rest)
	case "sessions":
		return cmdSessions(rest)
	case "stop":
		return cmdStop(rest)
	case "version", "-v", "--version":
		fmt.Println("biewer " + version)
		return 0
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "biewer: unknown command %q\n\n", cmd)
		printUsage(os.Stderr)
		return 1
	}
}

func printUsage(w *os.File) {
	fmt.Fprint(w, `biewer - a local resource supervisor for coding agents

Usage:
  biewer setup               configure and start Biewer (recommended first run)
  biewer enable              install & start the local daemon
  biewer disable              stop the local daemon
  biewer hooks install        wire up Claude Code hooks + a Codex shell wrapper
  biewer hooks uninstall      remove them again
  biewer telemetry install    send Claude metrics to the local OTLP receiver
  biewer telemetry status     check Claude telemetry configuration
  biewer telemetry uninstall  remove Biewer's Claude telemetry settings
  biewer watch                live view of tracked sessions (like top/docker stats)
  biewer tui                  interactive terminal dashboard
  biewer status                one-shot snapshot of tracked sessions
  biewer sessions [--limit N] session history
  biewer stop <session-id>    print a cleanup plan and terminate a session's processes
  biewer version               print the version

Run 'biewer setup' once, then use Claude or Codex normally. Setup starts the
daemon and installs optional hooks and local-only telemetry attribution.
`)
}

// biewerDir returns Biewer's home directory (~/.biewer), honoring
// BIEWER_HOME for tests/overrides.
func biewerDir() (string, error) {
	if v := os.Getenv("BIEWER_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".biewer"), nil
}
