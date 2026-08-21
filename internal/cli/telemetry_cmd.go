package cli

import (
	"fmt"
	"os"
	"sort"
)

// claudeTelemetryEnv is intentionally signal-specific: it sends Claude Code
// metrics and redacted lifecycle events to Biewer's loopback receiver without
// changing any trace exporter the user may already have configured.
var claudeTelemetryEnv = map[string]string{
	"CLAUDE_CODE_ENABLE_TELEMETRY":                      "1",
	"OTEL_METRICS_EXPORTER":                             "otlp",
	"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL":               "http/json",
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT":               "http://127.0.0.1:4318/v1/metrics",
	"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE": "delta",
	"OTEL_METRIC_EXPORT_INTERVAL":                       "10000",
	"OTEL_METRICS_INCLUDE_SESSION_ID":                   "true",
	"OTEL_LOGS_EXPORTER":                                "otlp",
	"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL":                  "http/json",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":                  "http://127.0.0.1:4318/v1/logs",
	"OTEL_LOG_USER_PROMPTS":                             "0",
	"OTEL_LOG_ASSISTANT_RESPONSES":                      "0",
	"OTEL_LOG_TOOL_DETAILS":                             "0",
	"OTEL_LOG_TOOL_CONTENT":                             "0",
	"OTEL_LOG_RAW_API_BODIES":                           "0",
}

func cmdTelemetry(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: biewer telemetry <install|status|uninstall>")
		return 2
	}
	path, err := claudeSettingsPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer telemetry:", err)
		return 1
	}
	switch args[0] {
	case "install":
		if err := installClaudeTelemetryAt(path); err != nil {
			fmt.Fprintln(os.Stderr, "biewer telemetry install:", err)
			return 1
		}
		fmt.Println("Installed Claude Code telemetry for Biewer")
		fmt.Printf("  settings: %s\n", path)
		fmt.Println("  metrics:  http://127.0.0.1:4318/v1/metrics (OTLP/HTTP JSON)")
		fmt.Println("  logs:     http://127.0.0.1:4318/v1/logs (content disabled)")
		fmt.Println("Restart Claude Code sessions for the settings to take effect.")
		return 0
	case "status":
		installed, mismatches, err := claudeTelemetryStatusAt(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "biewer telemetry status:", err)
			return 1
		}
		if installed {
			fmt.Println("Claude Code telemetry is configured for Biewer")
			return 0
		}
		fmt.Println("Claude Code telemetry is not fully configured for Biewer")
		for _, key := range mismatches {
			fmt.Printf("  %s\n", key)
		}
		return 1
	case "uninstall":
		if err := uninstallClaudeTelemetryAt(path); err != nil {
			fmt.Fprintln(os.Stderr, "biewer telemetry uninstall:", err)
			return 1
		}
		fmt.Println("Removed Biewer-managed Claude Code telemetry settings")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "biewer telemetry: unknown subcommand %q\n", args[0])
		return 2
	}
}

func installClaudeTelemetryAt(path string) error {
	root, err := loadJSONObject(path)
	if err != nil {
		return err
	}
	env, _ := root["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	var conflicts []string
	for key, wanted := range claudeTelemetryEnv {
		if existing, ok := env[key]; ok && fmt.Sprint(existing) != wanted {
			conflicts = append(conflicts, fmt.Sprintf("%s=%v (Biewer needs %s)", key, existing, wanted))
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("conflicting Claude telemetry settings:\n  %s", joinLines(conflicts))
	}
	for key, value := range claudeTelemetryEnv {
		env[key] = value
	}
	root["env"] = env
	return saveJSONObjectAtomic(path, root)
}

func claudeTelemetryStatusAt(path string) (bool, []string, error) {
	root, err := loadJSONObject(path)
	if err != nil {
		return false, nil, err
	}
	env, _ := root["env"].(map[string]any)
	var mismatches []string
	for key, wanted := range claudeTelemetryEnv {
		if fmt.Sprint(env[key]) != wanted {
			mismatches = append(mismatches, key)
		}
	}
	sort.Strings(mismatches)
	return len(mismatches) == 0, mismatches, nil
}

func uninstallClaudeTelemetryAt(path string) error {
	root, err := loadJSONObject(path)
	if err != nil {
		return err
	}
	env, _ := root["env"].(map[string]any)
	for key, installed := range claudeTelemetryEnv {
		if fmt.Sprint(env[key]) == installed {
			delete(env, key)
		}
	}
	if len(env) == 0 {
		delete(root, "env")
	} else {
		root["env"] = env
	}
	return saveJSONObjectAtomic(path, root)
}

func joinLines(lines []string) string {
	joined := ""
	for i, line := range lines {
		if i > 0 {
			joined += "\n  "
		}
		joined += line
	}
	return joined
}
