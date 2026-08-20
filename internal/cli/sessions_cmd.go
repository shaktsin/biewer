package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/shaktsin/biewer/internal/daemon"
	"github.com/shaktsin/biewer/internal/db"
)

func openStoreReadOnly(dir string) (*db.DB, error) {
	return db.Open(dir)
}

func cmdSessions(args []string) int {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "max sessions to show")
	eventsFor := fs.String("events", "", "print the recorded hook events for a session id/prefix instead of the history table")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer sessions:", err)
		return 1
	}

	if *eventsFor != "" {
		return printSessionEvents(dir, *eventsFor)
	}

	client := daemon.NewClient(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sessions, err := client.Sessions(ctx, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer sessions: daemon not reachable — run 'biewer enable' first:", err)
		return 1
	}
	if len(sessions) == 0 {
		fmt.Println("No session history yet.")
		return 0
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tAGENT\tPROJECT\tSTATE\tSTARTED\tDURATION")
	now := time.Now()
	for _, s := range sessions {
		end := now
		if s.EndedAt != nil {
			end = *s.EndedAt
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID(s.ID), s.Agent, truncate(s.Project, 24), s.State,
			s.StartedAt.Local().Format("Jan 2 15:04"), humanDuration(end.Sub(s.StartedAt)))
	}
	return runFlush(tw)
}

func runFlush(tw *tabwriter.Writer) int {
	if err := tw.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "biewer:", err)
		return 1
	}
	return 0
}

func printSessionEvents(dir, idOrPrefix string) int {
	// Reading events/resolving the id goes straight through the store
	// rather than the daemon socket, so `biewer sessions --events` still
	// works against history even if the daemon isn't currently running.
	d, err := openStoreReadOnly(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer sessions:", err)
		return 1
	}
	defer d.Close()

	sess, err := d.GetSession(context.Background(), idOrPrefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer sessions:", err)
		return 1
	}
	events, err := d.ReadEvents(sess.ID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer sessions:", err)
		return 1
	}
	for _, e := range events {
		line := fmt.Sprintf("%s  %-20s", e.Timestamp.Local().Format(time.RFC3339), e.Kind)
		if e.ToolName != "" {
			line += "  tool=" + e.ToolName
		}
		fmt.Println(line)
	}
	return 0
}

func cmdStop(args []string) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: biewer stop [--yes] <session-id>")
		return 2
	}
	id := fs.Arg(0)

	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer stop:", err)
		return 1
	}
	client := daemon.NewClient(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	plan, err := client.Stop(ctx, id, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer stop:", err)
		return 1
	}
	if len(plan.Pids) == 0 {
		fmt.Println("Nothing to stop: no attributed processes for this session.")
		return 0
	}

	fmt.Printf("Cleanup plan for session %s (%s):\n", shortID(plan.SessionID), plan.Project)
	for _, p := range plan.Pids {
		fmt.Printf("  pid %-8d %-10s %s\n", p.PID, humanBytes(p.RSSBytes), p.Command)
	}

	if !*yes {
		fmt.Print("Terminate these processes? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("Aborted.")
			return 0
		}
	}

	result, err := client.Stop(ctx, id, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer stop:", err)
		return 1
	}
	fmt.Printf("Signaled %d process(es): %v\n", len(result.Killed), result.Killed)
	return 0
}
