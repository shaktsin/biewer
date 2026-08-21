package cli

import (
	"fmt"
	"os"
)

type setupStep struct {
	name string
	run  func() int
}

func cmdSetup(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: biewer setup")
		return 2
	}

	fmt.Println("Setting up Biewer")
	code := runSetupSteps([]setupStep{
		{name: "local daemon", run: func() int { return cmdEnable(nil) }},
		{name: "Claude and Codex hooks", run: func() int { return cmdHooks([]string{"install"}) }},
		{name: "Claude telemetry", run: func() int { return cmdTelemetry([]string{"install"}) }},
	})
	if code != 0 {
		fmt.Fprintln(os.Stderr, "Biewer setup finished with errors; successful steps were kept and can be safely re-run.")
		return code
	}

	fmt.Println("Biewer is ready.")
	fmt.Println("Restart your shell (or source its rc file), then run: biewer tui")
	return 0
}

func runSetupSteps(steps []setupStep) int {
	failed := false
	for _, step := range steps {
		fmt.Printf("\n[%s]\n", step.name)
		if step.run() != 0 {
			failed = true
			fmt.Fprintf(os.Stderr, "biewer setup: %s failed\n", step.name)
		}
	}
	if failed {
		return 1
	}
	return 0
}
