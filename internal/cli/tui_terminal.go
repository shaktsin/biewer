package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	// Disable terminal auto-wrap while the TUI owns the alternate screen.
	// Every row is intentionally painted to the terminal's full width; leaving
	// DECAWM enabled can put some terminals in a pending-wrap state at the
	// bottom-right cell and make a later frame scroll instead of redraw.
	enterAltScreen = "\033[?1049h\033[?25l\033[?7l\033[2J\033[H"
	leaveAltScreen = "\033[0m\033[?7h\033[?25h\033[?1049l"
)

// tuiTerminal uses the platform's stty utility to put the terminal in raw
// mode. Biewer targets Unix hosts and already relies on Unix process APIs, so
// this keeps the TUI dependency-free without duplicating Darwin/Linux ioctl
// structures in the CLI.
type tuiTerminal struct {
	savedState string
}

func enterTUITerminal() (*tuiTerminal, error) {
	get := exec.Command("stty", "-g")
	get.Stdin = os.Stdin
	state, err := get.Output()
	if err != nil {
		return nil, fmt.Errorf("requires an interactive terminal: %w", err)
	}

	set := exec.Command("stty", "raw", "-echo")
	set.Stdin = os.Stdin
	if err := set.Run(); err != nil {
		return nil, fmt.Errorf("enable raw terminal mode: %w", err)
	}

	t := &tuiTerminal{savedState: strings.TrimSpace(string(state))}
	fmt.Print(enterAltScreen)
	return t, nil
}

func (t *tuiTerminal) restore() {
	if t == nil {
		return
	}
	fmt.Print(leaveAltScreen)
	args := strings.Fields(t.savedState)
	if len(args) == 0 {
		args = []string{"sane"}
	}
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

func tuiTerminalSize() (width, height int) {
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	if out, err := cmd.Output(); err == nil {
		var rows, cols int
		if _, err := fmt.Sscanf(string(out), "%d %d", &rows, &cols); err == nil && rows > 0 && cols > 0 {
			return cols, rows
		}
	}

	width, _ = strconv.Atoi(os.Getenv("COLUMNS"))
	height, _ = strconv.Atoi(os.Getenv("LINES"))
	if width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 32
	}
	return width, height
}
