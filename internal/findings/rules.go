// Package findings turns a session's attributed process tree into the
// human-readable diagnoses Biewer prints under a session in `biewer watch`
// (e.g. "vite remains active 18m after the last tool call").
//
// Rules here are pure functions of (session, process tree, now) so they can
// be unit tested without a running daemon or real OS processes.
package findings

import (
	"fmt"
	"strings"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

// Options configures the thresholds a rule fires at. Zero-value Options is
// replaced with DefaultOptions by Evaluate.
type Options struct {
	// IdleThreshold is how long a session's last recorded hook activity
	// (prompt submitted / tool used) must be in the past before a still-
	// listening dev-server-looking process is flagged.
	IdleThreshold time.Duration
	// MemoryThreshold is the total attributed RSS above which Biewer notes
	// the session's footprint (informational, not a warning).
	MemoryThreshold uint64
	// DevServerPatterns are case-insensitive substrings of a process
	// command line that mark it as a long-lived dev server / watcher
	// worth calling out by name when it outlives its session's activity.
	DevServerPatterns []string
}

// DefaultOptions are Biewer's out-of-the-box thresholds.
func DefaultOptions() Options {
	return Options{
		IdleThreshold:   10 * time.Minute,
		MemoryThreshold: 2 * 1024 * 1024 * 1024, // 2 GiB
		DevServerPatterns: []string{
			"vite", "next dev", "npm run dev", "yarn dev", "pnpm dev",
			"webpack-dev-server", "webpack serve", "flask run",
			"rails server", "rails s ", "django runserver", "ng serve",
			"nodemon", "air ", // air = Go live-reload
		},
	}
}

func withDefaults(o Options) Options {
	d := DefaultOptions()
	if o.IdleThreshold <= 0 {
		o.IdleThreshold = d.IdleThreshold
	}
	if o.MemoryThreshold == 0 {
		o.MemoryThreshold = d.MemoryThreshold
	}
	if len(o.DevServerPatterns) == 0 {
		o.DevServerPatterns = d.DevServerPatterns
	}
	return o
}

// Flatten walks a process tree (as returned by attribution) into a flat
// slice, root processes first.
func Flatten(tree []*model.Process) []*model.Process {
	var out []*model.Process
	var walk func(*model.Process)
	walk = func(p *model.Process) {
		out = append(out, p)
		for _, c := range p.Children {
			walk(c)
		}
	}
	for _, p := range tree {
		walk(p)
	}
	return out
}

func looksLikeDevServer(command string, patterns []string) (string, bool) {
	lower := strings.ToLower(command)
	for _, pat := range patterns {
		if strings.Contains(lower, strings.ToLower(pat)) {
			return strings.TrimSpace(pat), true
		}
	}
	return "", false
}

// Evaluate computes findings for one session given its currently-attributed
// process tree.
func Evaluate(session model.Session, tree []*model.Process, now time.Time, opts Options) []model.Finding {
	opts = withDefaults(opts)
	procs := Flatten(tree)

	var findings []model.Finding
	findings = append(findings, idleDevServerFindings(session, procs, now, opts)...)

	var totalRSS uint64
	for _, p := range procs {
		totalRSS += p.RSSBytes
	}
	if totalRSS > opts.MemoryThreshold {
		findings = append(findings, model.Finding{
			Severity:    model.SeverityInfo,
			Message:     fmt.Sprintf("session has %s attributed memory across %d processes", humanBytes(totalRSS), len(procs)),
			Attribution: "confirmed",
			Confidence:  model.ConfidenceConfirmed,
		})
	}

	return findings
}

func idleDevServerFindings(session model.Session, procs []*model.Process, now time.Time, opts Options) []model.Finding {
	if session.LastActivityAt.IsZero() {
		return nil
	}
	idleFor := now.Sub(session.LastActivityAt)
	if idleFor < opts.IdleThreshold {
		return nil
	}

	var out []model.Finding
	for _, p := range procs {
		if len(p.Ports) == 0 {
			continue
		}
		name, ok := looksLikeDevServer(p.Command, opts.DevServerPatterns)
		if !ok {
			continue
		}
		out = append(out, model.Finding{
			Severity: model.SeverityWarn,
			Message: fmt.Sprintf("%s remains active %s after the last tool call (pid %d, port %s)",
				name, humanDuration(idleFor), p.PID, joinPorts(p.Ports)),
			Attribution: "probable (recorded ancestry + hook timing)",
			Confidence:  model.ConfidenceProbable,
		})
	}
	return out
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func humanBytes(b uint64) string {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	case b >= mib:
		return fmt.Sprintf("%.0f MiB", float64(b)/mib)
	default:
		return fmt.Sprintf("%.0f KiB", float64(b)/kib)
	}
}

func joinPorts(ports []int) string {
	strs := make([]string, len(ports))
	for i, p := range ports {
		strs[i] = fmt.Sprintf("%d", p)
	}
	return strings.Join(strs, ",")
}
