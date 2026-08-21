package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shaktsin/biewer/internal/model"
)

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
	case b == 0:
		return "0 B"
	default:
		return fmt.Sprintf("%.0f KiB", float64(b)/kib)
	}
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func shortPorts(ports []int) string {
	if len(ports) == 0 {
		return "-"
	}
	strs := make([]string, len(ports))
	for i, p := range ports {
		strs[i] = fmt.Sprintf(":%d", p)
	}
	return strings.Join(strs, ",")
}

// renderSnapshot writes the `biewer watch` / `biewer status` view: a
// summary table (one row per session) followed by each session's process
// tree and findings, in the spirit of the project's README mockup.
func renderSnapshot(w io.Writer, snap model.Snapshot, now time.Time) {
	if len(snap.Sessions) == 0 {
		fmt.Fprintln(w, "No tracked sessions. Biewer auto-detects Claude Desktop, ChatGPT, and a bare claude/codex")
		fmt.Fprintln(w, "the moment they're running — if one is open and still shows nothing, see the README's")
		fmt.Fprintln(w, "auto-discovery patterns (process names can vary by install).")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tAGENT\tSESSION\tSRC\tATTR\tSTATE\tTOKENS\tCPU\tMEMORY\tPIDS\tPORTS")
	for _, ss := range snap.Sessions {
		s := ss.Session
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.1f%%\t%s\t%d\t%s\n",
			truncate(s.Project, 24), s.Agent, shortID(s.ID), s.Source, sessionAttribution(ss), stateLabel(ss, now),
			humanTokens(ss.Usage.TotalTokens), ss.CPUSeconds, humanBytes(ss.MemoryBytes),
			ss.ProcessCount, shortPorts(ss.ListenPorts))
	}
	tw.Flush()

	for _, ss := range snap.Sessions {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s (%s)\n", shortID(ss.Session.ID), ss.Session.Project)
		if len(ss.ProcessTree) == 0 {
			if ss.ResourceScope == model.ResourceNone {
				fmt.Fprintln(w, "  (logical task; process resources are not assigned to this thread)")
			} else {
				fmt.Fprintln(w, "  (no attributed processes)")
			}
		}
		for _, p := range ss.ProcessTree {
			renderProcessTree(w, p, "  ")
		}
		if len(ss.Findings) > 0 {
			fmt.Fprintln(w, "  FINDINGS")
			for _, f := range ss.Findings {
				fmt.Fprintf(w, "    %-4s  %s\n", f.Severity, f.Message)
				fmt.Fprintf(w, "          Attribution: %s\n", f.Attribution)
			}
		}
	}
}

func renderProcessTree(w io.Writer, p *model.Process, prefix string) {
	marker := "├─"
	if p.Depth == 0 {
		marker = ""
	}
	line := fmt.Sprintf("%s%s %s", prefix, marker, p.Command)
	fmt.Fprintf(w, "%-60s %10s", truncate(line, 60), humanBytes(p.RSSBytes))
	if len(p.Ports) > 0 {
		fmt.Fprintf(w, "  %s", shortPorts(p.Ports))
	}
	fmt.Fprintln(w)
	for _, c := range p.Children {
		renderProcessTree(w, c, prefix+"  ")
	}
}

func stateLabel(ss model.SessionSnapshot, now time.Time) string {
	if ss.Session.State == model.StateEnded {
		return "ended"
	}
	if ss.ProcessCount == 0 {
		if ss.ResourceScope == model.ResourceNone {
			return "observed"
		}
		return "orphaned?" // agent gone but session not marked ended (crash, or session_end hook missing)
	}
	return "active"
}

func shortID(id string) string {
	// Auto-discovered IDs are short process identifiers (auto-<PID>), not
	// UUIDs. Truncating them made distinct roots such as auto-44118 and
	// auto-44134 both appear as auto-441 in the TUI.
	if strings.HasPrefix(id, "auto-") {
		return id
	}
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
