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
	"time"
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
	if workingDirs, cwdErr := scanWorkingDirsDarwin(ctx); cwdErr == nil {
		for pid, cwd := range workingDirs {
			if process, ok := procs[pid]; ok {
				process.Cwd = cwd
				procs[pid] = process
			}
		}
	}
	ports, err := scanListenPortsDarwin(ctx)
	if err != nil {
		// Port discovery is best-effort (lsof can be slow/flaky under
		// load); don't fail the whole scan over it.
		ports = map[int][]int{}
	}
	return Snapshot{Processes: procs, ListenPorts: ports}, nil
}

func scanWorkingDirsDarwin(ctx context.Context) (map[int]string, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-n", "-d", "cwd", "-Fpn")
	var output bytes.Buffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil && output.Len() == 0 {
		return nil, err
	}

	workingDirs := make(map[int]string)
	currentPID := 0
	scanner := bufio.NewScanner(&output)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			currentPID, _ = strconv.Atoi(line[1:])
		case 'n':
			if currentPID > 0 {
				workingDirs[currentPID] = line[1:]
			}
		}
	}
	return workingDirs, scanner.Err()
}

func scanProcessesDarwin(ctx context.Context) (map[int]RawProcess, error) {
	cmd := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,pcpu=,lstart=,command=")
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
		if process, ok := parseDarwinProcessLine(sc.Text()); ok {
			procs[process.PID] = process
		}
	}
	return procs, sc.Err()
}

func parseDarwinProcessLine(line string) (RawProcess, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 10 {
		return RawProcess{}, false
	}
	pid, err1 := strconv.Atoi(fields[0])
	ppid, err2 := strconv.Atoi(fields[1])
	rssKB, err3 := strconv.ParseFloat(fields[2], 64)
	pcpu, err4 := strconv.ParseFloat(fields[3], 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return RawProcess{}, false
	}
	startedAt, _ := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(fields[4:9], " "), time.Local)
	return RawProcess{
		PID:       pid,
		PPID:      ppid,
		Command:   strings.Join(fields[9:], " "),
		StartedAt: startedAt,
		RSSBytes:  uint64(rssKB * 1024),
		CPUPct:    pcpu,
	}, true
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
