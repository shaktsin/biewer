// Package usage reads the token counters coding agents already write to their
// local JSONL transcripts and aggregates them by project. It never stores
// prompts, responses, or tool output.
package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

type Config struct {
	ClaudeRoot string
	CodexRoots []string
}

type Collector struct {
	cfg     Config
	sources map[string]model.UsageSource
}

func New(cfg Config, persisted []model.UsageSource) *Collector {
	if cfg.ClaudeRoot == "" || len(cfg.CodexRoots) == 0 {
		if home, err := os.UserHomeDir(); err == nil {
			if cfg.ClaudeRoot == "" {
				cfg.ClaudeRoot = filepath.Join(home, ".claude", "projects")
			}
			if len(cfg.CodexRoots) == 0 {
				cfg.CodexRoots = []string{
					filepath.Join(home, ".codex", "sessions"),
					filepath.Join(home, ".codex", "archived_sessions"),
				}
			}
		}
	}
	c := &Collector{cfg: cfg, sources: make(map[string]model.UsageSource, len(persisted))}
	for _, source := range persisted {
		c.sources[source.Path] = source
	}
	return c
}

// Scan refreshes changed transcript files and returns the full persisted
// source index plus project aggregates. Errors in individual files are skipped
// so a partially-written active transcript cannot break the daemon loop.
func (c *Collector) Scan(ctx context.Context) ([]model.ProjectUsage, []model.UsageSource, error) {
	if err := c.scanRoot(ctx, c.cfg.ClaudeRoot, model.AgentClaude, parseClaude); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}
	for _, root := range c.cfg.CodexRoots {
		if err := c.scanRoot(ctx, root, model.AgentCodex, parseCodex); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
	}

	sources := make([]model.UsageSource, 0, len(c.sources))
	for _, source := range c.sources {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	return aggregateProjects(sources), sources, nil
}

type parser func(string) (model.UsageSource, error)

func (c *Collector) scanRoot(ctx context.Context, root string, agent model.Agent, parse parser) error {
	if root == "" {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if previous, ok := c.sources[path]; ok && previous.Size == info.Size() && previous.ModifiedUnixNano == info.ModTime().UnixNano() && !previous.UpdatedAt.IsZero() && !previous.StartedAt.IsZero() {
			return nil
		}
		source, err := parse(path)
		if err != nil || source.Cwd == "" || source.Usage.TotalTokens == 0 {
			return nil
		}
		source.Path = path
		source.Agent = agent
		source.Size = info.Size()
		source.ModifiedUnixNano = info.ModTime().UnixNano()
		if source.UpdatedAt.IsZero() {
			source.UpdatedAt = info.ModTime()
		}
		if source.StartedAt.IsZero() {
			source.StartedAt = source.UpdatedAt
		}
		source.Archived = strings.Contains(filepath.ToSlash(path), "/archived_sessions/")
		source.Project = projectName(source.Cwd)
		c.sources[path] = source
		return nil
	})
}

func parseClaude(path string) (model.UsageSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.UsageSource{}, err
	}
	defer f.Close()

	type usageFields struct {
		Input       uint64 `json:"input_tokens"`
		CacheRead   uint64 `json:"cache_read_input_tokens"`
		CacheCreate uint64 `json:"cache_creation_input_tokens"`
		Output      uint64 `json:"output_tokens"`
	}
	type record struct {
		SessionID string    `json:"sessionId"`
		Cwd       string    `json:"cwd"`
		Timestamp time.Time `json:"timestamp"`
		Message   struct {
			ID    string      `json:"id"`
			Role  string      `json:"role"`
			Usage usageFields `json:"usage"`
		} `json:"message"`
	}

	var source model.UsageSource
	messages := make(map[string]usageFields)
	scanner := jsonlScanner(f)
	for scanner.Scan() {
		var rec record
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			continue
		}
		if rec.SessionID != "" {
			source.SessionID = rec.SessionID
		}
		if rec.Cwd != "" {
			source.Cwd = rec.Cwd
		}
		if !rec.Timestamp.IsZero() {
			if source.StartedAt.IsZero() {
				source.StartedAt = rec.Timestamp
			}
			if rec.Timestamp.After(source.UpdatedAt) {
				source.UpdatedAt = rec.Timestamp
			}
		}
		if rec.Message.Role == "assistant" && rec.Message.ID != "" {
			messages[rec.Message.ID] = rec.Message.Usage
		}
	}
	if err := scanner.Err(); err != nil {
		return model.UsageSource{}, err
	}
	for _, usage := range messages {
		source.Usage.InputTokens += usage.Input
		source.Usage.CachedInputTokens += usage.CacheRead
		source.Usage.CacheWriteTokens += usage.CacheCreate
		source.Usage.OutputTokens += usage.Output
	}
	source.Usage.TotalTokens = source.Usage.InputTokens + source.Usage.CachedInputTokens + source.Usage.CacheWriteTokens + source.Usage.OutputTokens
	return source, nil
}

func parseCodex(path string) (model.UsageSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return model.UsageSource{}, err
	}
	defer f.Close()

	type tokenFields struct {
		Input     uint64 `json:"input_tokens"`
		Cached    uint64 `json:"cached_input_tokens"`
		Output    uint64 `json:"output_tokens"`
		Reasoning uint64 `json:"reasoning_output_tokens"`
		Total     uint64 `json:"total_tokens"`
	}
	type record struct {
		Timestamp time.Time `json:"timestamp"`
		Type      string    `json:"type"`
		Payload   struct {
			Type  string `json:"type"`
			ID    string `json:"id"`
			Cwd   string `json:"cwd"`
			Model string `json:"model"`
			Info  struct {
				Total tokenFields `json:"total_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}

	var source model.UsageSource
	var latest tokenFields
	scanner := jsonlScanner(f)
	for scanner.Scan() {
		var rec record
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			continue
		}
		if rec.Type == "session_meta" {
			source.SessionID = rec.Payload.ID
			source.Cwd = rec.Payload.Cwd
			if source.StartedAt.IsZero() {
				source.StartedAt = rec.Timestamp
			}
		}
		if rec.Payload.Model != "" {
			source.Model = rec.Payload.Model
		}
		if !rec.Timestamp.IsZero() && rec.Timestamp.After(source.UpdatedAt) {
			source.UpdatedAt = rec.Timestamp
		}
		if rec.Type == "event_msg" && rec.Payload.Type == "token_count" && rec.Payload.Info.Total.Total > 0 {
			latest = rec.Payload.Info.Total
		}
	}
	if err := scanner.Err(); err != nil {
		return model.UsageSource{}, err
	}
	uncached := latest.Input
	if latest.Cached <= uncached {
		uncached -= latest.Cached
	}
	source.Usage = model.TokenUsage{
		InputTokens: uncached, CachedInputTokens: latest.Cached,
		OutputTokens: latest.Output, ReasoningTokens: latest.Reasoning, TotalTokens: latest.Total,
	}
	return source, nil
}

func jsonlScanner(file *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return scanner
}

func aggregateProjects(sources []model.UsageSource) []model.ProjectUsage {
	// A transcript can move from sessions/ to archived_sessions/. Deduplicate by
	// provider session ID, preferring the most recently modified copy.
	dedup := DeduplicateSources(sources)

	byCwd := make(map[string]*model.ProjectUsage)
	for _, source := range dedup {
		project := byCwd[source.Cwd]
		if project == nil {
			project = &model.ProjectUsage{Project: source.Project, Cwd: source.Cwd}
			byCwd[source.Cwd] = project
		}
		addUsage(&project.Usage, source.Usage)
		project.Sessions++
		modified := time.Unix(0, source.ModifiedUnixNano)
		if modified.After(project.UpdatedAt) {
			project.UpdatedAt = modified
		}
	}

	out := make([]model.ProjectUsage, 0, len(byCwd))
	for _, project := range byCwd {
		out = append(out, *project)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Usage.TotalTokens == out[j].Usage.TotalTokens {
			return out[i].Project < out[j].Project
		}
		return out[i].Usage.TotalTokens > out[j].Usage.TotalTokens
	})
	return out
}

// DeduplicateSources collapses active/archived copies of the same provider
// transcript, preferring the newest copy. The returned map is keyed by
// "agent:session-id" (or by path when a provider omitted its session ID).
func DeduplicateSources(sources []model.UsageSource) map[string]model.UsageSource {
	dedup := make(map[string]model.UsageSource)
	for _, source := range sources {
		key := string(source.Agent) + ":" + source.SessionID
		if source.SessionID == "" {
			key = source.Path
		}
		prior, ok := dedup[key]
		if !ok || source.ModifiedUnixNano > prior.ModifiedUnixNano {
			dedup[key] = source
		}
	}
	return dedup
}

func addUsage(total *model.TokenUsage, usage model.TokenUsage) {
	total.InputTokens += usage.InputTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.CacheWriteTokens += usage.CacheWriteTokens
	total.OutputTokens += usage.OutputTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
}

func projectName(cwd string) string {
	name := filepath.Base(filepath.Clean(cwd))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "unknown"
	}
	return name
}
