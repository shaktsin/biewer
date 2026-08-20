package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shaktsin/biewer/internal/daemon"
)

// detachedSysProcAttr starts the daemon in its own session so it survives
// the CLI invocation exiting (no controlling terminal to be killed with).
// Setsid is present in syscall.SysProcAttr on both of Biewer's Unix
// targets (darwin, linux), so no build tags are needed here.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func readPidFile(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	return pid, true
}

func cmdEnable(_ []string) int {
	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer enable:", err)
		return 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "biewer enable: create", dir, ":", err)
		return 1
	}

	pidPath := daemon.PidFilePath(dir)
	if pid, ok := readPidFile(pidPath); ok && isProcessAlive(pid) {
		fmt.Printf("Local daemon already running (pid %d)\n", pid)
		return 0
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer enable: locate self:", err)
		return 1
	}
	logPath := daemon.LogFilePath(dir)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer enable: open log file:", err)
		return 1
	}
	defer logFile.Close()

	cmd := exec.Command(self, "daemon-run")
	cmd.Env = append(os.Environ(), "BIEWER_HOME="+dir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = detachedSysProcAttr()

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "biewer enable: start daemon:", err)
		return 1
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "biewer enable: write pidfile:", err)
		return 1
	}
	_ = cmd.Process.Release()

	// Confirm it actually came up before declaring success.
	client := daemon.NewClient(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var upErr error
	for i := 0; i < 15; i++ {
		if upErr = client.Healthz(ctx); upErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if upErr != nil {
		fmt.Fprintf(os.Stderr, "biewer enable: daemon did not come up (check %s): %v\n", logPath, upErr)
		return 1
	}

	fmt.Println("Installed local daemon")
	fmt.Printf("  socket: %s\n", daemon.SocketPath(dir))
	fmt.Printf("  log:    %s\n", logPath)
	return 0
}

func cmdDisable(_ []string) int {
	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer disable:", err)
		return 1
	}
	pidPath := daemon.PidFilePath(dir)
	pid, ok := readPidFile(pidPath)
	if !ok || !isProcessAlive(pid) {
		fmt.Println("Local daemon is not running")
		_ = os.Remove(pidPath)
		return 0
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Fprintln(os.Stderr, "biewer disable: signal daemon:", err)
		return 1
	}
	for i := 0; i < 25; i++ {
		if !isProcessAlive(pid) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = os.Remove(pidPath)
	_ = os.Remove(daemon.SocketPath(dir))
	fmt.Println("Stopped local daemon")
	return 0
}

// cmdDaemonRun is the internal entrypoint spawned (detached) by `enable`. It
// runs the daemon in the foreground of its own process until SIGTERM/SIGINT.
func cmdDaemonRun(_ []string) int {
	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer daemon-run:", err)
		return 1
	}

	d, err := daemon.New(daemon.Config{Dir: dir})
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer daemon-run: init:", err)
		return 1
	}
	defer d.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := d.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "biewer daemon-run: serve:", err)
		return 1
	}
	return 0
}

func cmdStatus(_ []string) int {
	dir, err := biewerDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer status:", err)
		return 1
	}
	client := daemon.NewClient(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Healthz(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "biewer status: daemon not reachable — run 'biewer enable' first")
		return 1
	}
	snap, err := client.Snapshot(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "biewer status:", err)
		return 1
	}
	renderSnapshot(os.Stdout, snap, time.Now())
	return 0
}
