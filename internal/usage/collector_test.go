package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectorAggregatesClaudeAndCodexByProject(t *testing.T) {
	root := t.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	codexRoot := filepath.Join(root, "codex")
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := `{"sessionId":"claude-1","cwd":"/work/app","message":{"id":"msg-1","role":"assistant","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":5,"output_tokens":3}}}` + "\n"
	codex := `{"type":"session_meta","payload":{"id":"codex-1","cwd":"/work/app"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":10,"reasoning_output_tokens":2,"total_tokens":110}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(claudeRoot, "c.jsonl"), []byte(claude), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexRoot, "x.jsonl"), []byte(codex), 0o644); err != nil {
		t.Fatal(err)
	}

	collector := New(Config{ClaudeRoot: claudeRoot, CodexRoots: []string{codexRoot}}, nil)
	projects, sources, err := collector.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || len(projects) != 1 {
		t.Fatalf("sources=%d projects=%d", len(sources), len(projects))
	}
	got := projects[0]
	if got.Project != "app" || got.Sessions != 2 || got.Usage.TotalTokens != 148 {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
	if got.Usage.CachedInputTokens != 60 || got.Usage.CacheWriteTokens != 5 || got.Usage.ReasoningTokens != 2 {
		t.Fatalf("unexpected breakdown: %+v", got.Usage)
	}
}

func TestClaudeDuplicateMessageIsNotDoubleCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := `{"sessionId":"s1","cwd":"/work/app","message":{"id":"same","role":"assistant","usage":{"input_tokens":7,"output_tokens":3}}}` + "\n"
	if err := os.WriteFile(path, []byte(line+line), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := parseClaude(path)
	if err != nil {
		t.Fatal(err)
	}
	if source.Usage.TotalTokens != 10 {
		t.Fatalf("duplicate assistant message counted twice: %+v", source.Usage)
	}
}
