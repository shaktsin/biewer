//go:build darwin

package procscan

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// DarwinScanner scans the process table via `ps` and listening TCP ports
// via `lsof`. Both are available on stock macOS with no elevated
// privileges; this is Biewer's native-mode scanner.
type DarwinScanner struct{}

// NewScanner returns the platform Scanner for the current GOOS. On darwin
// this is DarwinScanner.
func NewScanner() Scanner { return DarwinScanner{} }

func (DarwinScanner) Scan(ctx context.Context) (Snapshot, error) {
	procs, err := scanProcessesDarwin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	ports, err := scanListenPortsDarwin(ctx)
	if err != nil {
		// Port discovery is best-effort (lsof can be slow/flaky under
		// load); don't fail the whole scan over it.
		ports = map[int][]int{}
	}
	return Snapshot{Processes: procs, ListenPorts: ports}, nil
}

func scanProcessesDarwin(ctx context.Context) (map[int]RawProcess, error) {
	cmd := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,pcpu=,command=")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ps: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	procs := make(map[int]RawProcess)
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		rssKB, err3 := strconv.ParseFloat(fields[2], 64)
		pcpu, err4 := strconv.ParseFloat(fields[3], 64)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			continue
		}
		command := strings.Join(fields[4:], " ")
		procs[pid] = RawProcess{
			PID:      pid,
			PPID:     ppid,
			Command:  command,
			RSSBytes: uint64(rssKB * 1024),
			CPUPct:   pcpu,
		}
	}
	return procs, sc.Err()
}

func scanListenPortsDarwin(ctx context.Context) (map[int][]int, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN")
	var out bytes.Buffer
	cmd.Stdout = &out
	// lsof exits non-zero when it has nothing to report on some systems;
	// only treat it as fatal if we truly got no usable output.
	_ = cmd.Run()

	ports := make(map[int][]int)
	sc := bufio.NewScanner(&out)
	first := true
	for sc.Scan() {
		if first { // header line: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 9 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		name := fields[len(fields)-2] // e.g. "*:5173" or "127.0.0.1:5173"; last field is "(LISTEN)"
		idx := strings.LastIndex(name, ":")
		if idx < 0 || idx == len(name)-1 {
			continue
		}
		port, err := strconv.Atoi(name[idx+1:])
		if err != nil {
			continue
		}
		ports[pid] = append(ports[pid], port)
	}
	return ports, nil
}
