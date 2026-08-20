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
)

const clearScreen = "\033[H\033[2J"

func cmdWatch(args []string) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	interval := fs.Duration("interval", 2*time.Second, "refresh interval")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer watch:", err)
		return 1
	}
	client := daemon.NewClient(dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		reqCtx, reqCancel := context.WithTimeout(ctx, 3*time.Second)
		snap, err := client.Snapshot(reqCtx)
		reqCancel()

		fmt.Print(clearScreen)
		fmt.Printf("biewer watch — refreshing every %s (Ctrl-C to exit)\n\n", interval.String())
		if err != nil {
			fmt.Fprintln(os.Stdout, "daemon not reachable — run 'biewer enable' first")
		} else {
			renderSnapshot(os.Stdout, snap, time.Now())
		}

		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
	}
}
