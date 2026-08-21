package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeTelemetryInstallStatusAndUninstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","env":{"KEEP_ME":"yes"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeTelemetryAt(path); err != nil {
		t.Fatal(err)
	}
	installed, mismatches, err := claudeTelemetryStatusAt(path)
	if err != nil || !installed || len(mismatches) != 0 {
		t.Fatalf("telemetry status: installed=%v mismatches=%v err=%v", installed, mismatches, err)
	}
	root, err := loadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	env := root["env"].(map[string]any)
	if env["KEEP_ME"] != "yes" || root["theme"] != "dark" {
		t.Fatalf("install did not preserve existing settings: %+v", root)
	}

	if err := uninstallClaudeTelemetryAt(path); err != nil {
		t.Fatal(err)
	}
	root, err = loadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	env = root["env"].(map[string]any)
	if len(env) != 1 || env["KEEP_ME"] != "yes" || root["theme"] != "dark" {
		t.Fatalf("uninstall removed unrelated settings: %+v", root)
	}
}

func TestClaudeTelemetryInstallRefusesConflictingExporter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"env":{"OTEL_METRICS_EXPORTER":"console"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeTelemetryAt(path); err == nil {
		t.Fatal("expected conflicting telemetry exporter to be rejected")
	}
	root, err := loadJSONObject(path)
	if err != nil {
		t.Fatal(err)
	}
	env := root["env"].(map[string]any)
	if env["OTEL_METRICS_EXPORTER"] != "console" || len(env) != 1 {
		t.Fatalf("conflicting settings were modified: %+v", root)
	}
}
