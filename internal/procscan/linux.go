//go:build linux

package procscan

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LinuxScanner scans the process table and listening ports via /proc. It is
// used for local development/testing of the attribution and findings
// logic, for Biewer running directly on Linux hosts, and is the intended
// basis for the future managed-mode guest probe (which also has /proc
// available inside its cgroup).
//
// ProcRoot defaults to "/proc" but is overridable for tests.
type LinuxScanner struct {
	ProcRoot string
}

// NewScanner returns the platform Scanner for the current GOOS. On linux
// this is a LinuxScanner rooted at /proc.
func NewScanner() Scanner { return LinuxScanner{ProcRoot: "/proc"} }

func (l LinuxScanner) root() string {
	if l.ProcRoot == "" {
		return "/proc"
	}
	return l.ProcRoot
}

func (l LinuxScanner) Scan(ctx context.Context) (Snapshot, error) {
	procs, err := l.scanProcesses()
	if err != nil {
		return Snapshot{}, err
	}
	ports, err := l.scanListenPorts()
	if err != nil {
		ports = map[int][]int{}
	}
	return Snapshot{Processes: procs, ListenPorts: ports}, nil
}

func (l LinuxScanner) scanProcesses() (map[int]RawProcess, error) {
	entries, err := os.ReadDir(l.root())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", l.root(), err)
	}
	procs := make(map[int]RawProcess)
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid directory
		}
		p, ok := l.readProcess(pid)
		if ok {
			procs[pid] = p
		}
	}
	return procs, nil
}

// readProcess reads /proc/<pid>/stat and /proc/<pid>/status for one
// process. It returns ok=false for processes that exited between the
// directory listing and the read (a normal race, not an error).
func (l LinuxScanner) readProcess(pid int) (RawProcess, bool) {
	statPath := filepath.Join(l.root(), strconv.Itoa(pid), "stat")
	raw, err := os.ReadFile(statPath)
	if err != nil {
		return RawProcess{}, false
	}
	// Format: pid (comm) state ppid ...  comm may contain spaces/parens, so
	// split on the last ')' to get past it reliably.
	s := string(raw)
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < 0 || close < open {
		return RawProcess{}, false
	}
	comm := s[open+1 : close]
	rest := strings.Fields(s[close+1:])
	if len(rest) < 2 {
		return RawProcess{}, false
	}
	ppid, err := strconv.Atoi(rest[1]) // rest[0] = state, rest[1] = ppid
	if err != nil {
		return RawProcess{}, false
	}

	// Prefer the full cmdline (matches `ps command=` more closely, e.g.
	// "npm run dev" rather than just "npm").
	command := comm
	if cmdline, err := os.ReadFile(filepath.Join(l.root(), strconv.Itoa(pid), "cmdline")); err == nil && len(cmdline) > 0 {
		parts := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		command = strings.Join(parts, " ")
	}

	var rssKB uint64
	if status, err := os.ReadFile(filepath.Join(l.root(), strconv.Itoa(pid), "status")); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(status)))
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
						rssKB = v
					}
				}
				break
			}
		}
	}

	return RawProcess{
		PID:      pid,
		PPID:     ppid,
		Command:  command,
		RSSBytes: rssKB * 1024,
		// CPUPct is intentionally left at 0 here: an accurate percentage
		// requires sampling /proc/<pid>/stat utime+stime across two ticks
		// and dividing by elapsed wall time. macOS's `ps` computes this for
		// us; the Linux scanner is currently used for dev/testing and
		// non-macOS hosts where this is a known gap, not for the primary
		// (darwin) target.
		CPUPct: 0,
	}, true
}

// scanListenPorts parses /proc/net/tcp[6] for sockets in LISTEN state
// (st == 0A) and maps their inode back to an owning pid by walking each
// process's /proc/<pid>/fd/* symlinks.
func (l LinuxScanner) scanListenPorts() (map[int][]int, error) {
	inodeToPort := map[string]int{}
	for _, f := range []string{"net/tcp", "net/tcp6"} {
		l.collectListenInodes(filepath.Join(l.root(), f), inodeToPort)
	}
	if len(inodeToPort) == 0 {
		return map[int][]int{}, nil
	}

	ports := map[int][]int{}
	entries, err := os.ReadDir(l.root())
	if err != nil {
		return ports, err
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(l.root(), e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // permission denied for other users' processes, etc.
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if port, ok := inodeToPort[inode]; ok {
				ports[pid] = append(ports[pid], port)
			}
		}
	}
	return ports, nil
}

func (l LinuxScanner) collectListenInodes(path string, out map[string]int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue // header
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		localAddr := fields[1] // "0100007F:1F90"
		state := fields[3]     // "0A" == TCP_LISTEN
		inode := fields[9]
		if state != "0A" {
			continue
		}
		parts := strings.Split(localAddr, ":")
		if len(parts) != 2 {
			continue
		}
		portVal, err := strconv.ParseInt(parts[1], 16, 32)
		if err != nil {
			continue
		}
		out[inode] = int(portVal)
	}
}
