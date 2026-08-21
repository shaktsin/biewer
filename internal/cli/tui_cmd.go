package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shaktsin/biewer/internal/daemon"
	"github.com/shaktsin/biewer/internal/model"
)

func cmdTUI(args []string) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	interval := fs.Duration("interval", 2*time.Second, "refresh interval")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: biewer tui [--interval 2s]")
		return 2
	}
	if *interval < 250*time.Millisecond {
		fmt.Fprintln(os.Stderr, "biewer tui: --interval must be at least 250ms")
		return 2
	}

	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer tui:", err)
		return 1
	}
	term, err := enterTUITerminal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer tui:", err)
		return 1
	}
	defer term.restore()

	client := daemon.NewClient(dir)
	var snap model.Snapshot
	var fetchErr error
	selected := 0
	selectedKey := ""
	lastRefresh := time.Now()
	sessionHistory := make(map[string][]tuiSample)
	fetch := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		var next model.Snapshot
		next, fetchErr = client.Snapshot(ctx)
		lastRefresh = time.Now()
		if fetchErr == nil {
			snap = next
			liveSessions := make(map[string]struct{}, len(next.Sessions))
			for _, session := range next.Sessions {
				if session.Session.ID == "" {
					continue
				}
				key := tuiSessionKey(session)
				liveSessions[key] = struct{}{}
				sessionHistory[key] = appendTUISample(
					sessionHistory[key],
					sessionTUISample(session, lastRefresh),
					40,
				)
			}
			for sessionID := range sessionHistory {
				if _, exists := liveSessions[sessionID]; !exists {
					delete(sessionHistory, sessionID)
				}
			}
			selected, selectedKey = reconcileTUISelection(snap.Sessions, selectedKey, selected)
		}
	}
	fetch()
	selectAt := func(index int) {
		selected, selectedKey = reconcileTUISelection(snap.Sessions, "", index)
	}

	input := make(chan byte, 16)
	go func() {
		buf := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				close(input)
				return
			}
			input <- buf[0]
		}
	}()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	render := func() {
		width, height := tuiTerminalSize()
		view := tuiView{
			Snapshot:       snap,
			SessionHistory: sessionHistory,
			Selected:       selected,
			Width:          width,
			Height:         height,
			Now:            time.Now(),
			LastRefresh:    lastRefresh,
			Connected:      fetchErr == nil,
			Color:          os.Getenv("NO_COLOR") == "",
		}
		_, _ = os.Stdout.Write([]byte(renderTUIFrame(view)))
	}
	render()

	// Arrow keys arrive as ESC [ A/B. A short timeout lets a bare Escape
	// remain a quit key without swallowing a partially-read arrow sequence.
	escapeState := 0
	var escapeDeadline <-chan time.Time
	for {
		select {
		case b, ok := <-input:
			if !ok {
				return 0
			}
			switch escapeState {
			case 1:
				if b == '[' {
					escapeState = 2
					escapeDeadline = time.After(80 * time.Millisecond)
					continue
				}
				return 0
			case 2:
				escapeState = 0
				escapeDeadline = nil
				if b == 'A' {
					selectAt(selected - 1)
				} else if b == 'B' {
					selectAt(selected + 1)
				}
				render()
				continue
			}

			switch b {
			case 3, 'q', 'Q': // Ctrl-C or q
				return 0
			case 27:
				escapeState = 1
				escapeDeadline = time.After(80 * time.Millisecond)
			case 'j':
				selectAt(selected + 1)
				render()
			case 'k':
				selectAt(selected - 1)
				render()
			case 'g':
				selectAt(0)
				render()
			case 'G':
				selectAt(len(snap.Sessions) - 1)
				render()
			case 'r', 'R':
				fetch()
				render()
			}
		case <-escapeDeadline:
			return 0
		case sig := <-sigCh:
			if sig == syscall.SIGTERM {
				return 0
			}
			render()
		case <-ticker.C:
			fetch()
			render()
		}
	}
}

func tuiSessionKey(session model.SessionSnapshot) string {
	return string(session.Session.Agent) + ":" + session.Session.ID
}

func reconcileTUISelection(sessions []model.SessionSnapshot, selectedKey string, fallbackIndex int) (int, string) {
	if len(sessions) == 0 {
		return 0, ""
	}
	if selectedKey != "" {
		for index, session := range sessions {
			if tuiSessionKey(session) == selectedKey {
				return index, selectedKey
			}
		}
	}
	fallbackIndex = minInt(maxInt(0, fallbackIndex), len(sessions)-1)
	return fallbackIndex, tuiSessionKey(sessions[fallbackIndex])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
