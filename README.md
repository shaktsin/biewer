# Biewer

> **Keep your coding agents' processes on a short leash.**

Biewer is a local resource supervisor for Claude Code, OpenAI Codex, and other coding agents. It observes what each agent session spawns, attributes CPU, memory, ports, and process count to that session, flags things like a dev server left running after the agent went idle, and gives you a safe, explicit plan before it kills anything.

Biewer is local-first and dependency-free. It does not proxy model traffic, require an API key, upload transcripts, or depend on a hosted control plane. There is no embedded database engine either — session history is plain JSON files under `~/.biewer/`, readable with `cat`.

```console
$ biewer enable
Installed local daemon

$ biewer hooks install
Installed Claude and Codex hooks

$ claude   # or: codex
```

In another terminal, Biewer follows the session from outside:

```console
$ biewer watch

PROJECT   AGENT   SESSION   STATE   CPU    MEMORY   PEAK    PIDS  PORTS
my-app    codex   0195d17a  active  4.2%   612 MiB   1.1 GiB  6    :5173

0195d17a (my-app)
   codex                                                       220 MiB
     npm run dev                                                18 MiB
       node                                                    374 MiB  :5173

  FINDINGS
    WARN  vite remains active 18m after the last tool call (pid 5512, port 5173)
          Attribution: probable (recorded ancestry + hook timing)
```

## Status

This is a working MVP, built in one session against the original product proposal below. It covers native-mode tracking end to end: daemon, hook wiring for both agents, live watch/status, session history, and a confirm-before-kill cleanup flow. **Managed mode (cgroup/VM-enforced ownership) is not implemented** — see [What's not built yet](#whats-not-built-yet). Naming/trademark clearance for public release has not been repeated.

## Install

Requires Go 1.24+ to build from source (no other dependencies — the module has zero external `require`s).

```console
$ make build          # builds ./dist/biewer for your current OS/arch
$ make install         # copies it to ~/.local/bin (make sure that's on PATH)
```

Or cross-compile for another target (no C toolchain needed, cgo is never used):

```console
$ make build-all       # darwin/arm64, darwin/amd64, linux/amd64, linux/arm64
```

## Usage

```console
$ biewer enable                # start the local daemon (idempotent)
$ biewer hooks install         # wire up Claude Code hooks + a Codex shell wrapper
$ biewer watch                  # live view, refreshes every 2s, Ctrl-C to exit
$ biewer status                  # one-shot snapshot, no loop
$ biewer sessions [--limit N]    # session history (agent, project, duration, state)
$ biewer sessions --events <id>  # the recorded hook events for one session
$ biewer stop <session-id>       # print a kill plan, confirm, then terminate it
$ biewer disable                 # stop the daemon
$ biewer hooks uninstall         # remove the Claude hooks + Codex wrapper again
```

`biewer stop` never kills anything without printing the plan first (every attributed pid, its command, and its memory) and asking for confirmation — pass `--yes` to skip the prompt in scripts.

## How attribution works

Biewer never proxies or intercepts your agent's traffic. It learns a session's identity two ways:

- **Claude Code**: native hooks (`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `SessionEnd`) wired into `~/.claude/settings.json`. `SessionStart` also captures `$PPID`, i.e. Claude Code's own process ID, giving Biewer an exact root PID with no guessing.
- **Codex**: a shell function wrapper installed into your `~/.zshrc` (or `~/.bashrc`/`~/.bash_profile`) that backgrounds the real `codex` binary, captures its PID via `$!`, and reports session start/end around it. Codex does not currently get the same per-tool activity granularity Claude Code does — see below.

From a session's root PID, the daemon rescans the OS process table roughly every 2 seconds and walks live parent→child links to find everything still attributed to that session — this is "confirmed" attribution in the project's original evidence-hierarchy design. Listening TCP ports are matched to attributed processes via `lsof` (native mode) so a lingering `vite`/`next dev`/etc. can be named specifically in findings.

Idle detection compares "now" against the session's last recorded hook activity (a prompt submitted or a tool used), not just whether the agent binary is still running — that's what lets Biewer distinguish "the agent is still actively working" from "the agent process is alive but idle, and it left a dev server behind."

## What's not built yet

Being direct about the gaps rather than letting the demo output imply more than what's here:

- **Managed mode** (`biewer attach ... --install-guest`, cgroup/VM-enforced containment) from the original proposal is not implemented. Everything here is native mode only.
- **Reparented/double-forked processes**: once a process is adopted by PID 1 (detached from the live process tree), pure lineage-based attribution loses it — there's a unit test (`TestSnapshot_Descendants_ReparentedProcessIsLost`) documenting this on purpose. The original design's `cwd`/port corroboration fallback for "probable" confidence in that case isn't implemented yet.
- **Codex per-tool activity**: only session start/end are tracked for Codex today (via the shell wrapper), not individual tool calls, because Codex's current hook/notify schema wasn't something this session could verify with confidence. Idle detection for Codex sessions is therefore coarser than for Claude Code sessions.
- **I/O accounting**: CPU and memory are tracked; disk/network I/O is not (macOS has no simple unprivileged per-process I/O counter; `fs_usage`/`powermetrics` need sudo).
- **Windows**: not a target. Process scanning, signaling, and the shell wrapper all assume a Unix-like OS (darwin or linux).
- **Linux CPU%**: the Linux process scanner (used for local dev/testing here, and usable on real Linux hosts) reports 0% CPU rather than a sampled percentage — see the comment in `internal/procscan/linux.go`. macOS's `ps` computes this for us natively, which is the primary target.

## Project layout

```
cmd/biewer/            CLI entrypoint
internal/model/        shared types (Session, Event, Process, Finding, Snapshot)
internal/procscan/      process table + listening port scanning (darwin via ps/lsof, linux via /proc)
internal/findings/      pure, unit-tested rules (idle dev server, high memory)
internal/db/            local JSON-file session history + event log (no embedded DB engine)
internal/daemon/        the supervisor: HTTP API over a Unix socket, scan/attribution loop, kill plans
internal/cli/           `biewer` subcommands, hook installers, table/tree rendering
```

## Development

```console
$ make test    # go test ./...
$ make vet     # go vet ./...
$ make fmt     # gofmt -l -w .
```

The process scanner and findings engine are platform-agnostic-by-design and unit tested against fake process tables (`internal/procscan/linux_test.go` builds a fake `/proc`, `internal/daemon/daemon_test.go` injects a fake scanner) rather than depending on real OS processes, so `go test ./...` is fast and deterministic in CI or a sandboxed container — no root, no real agent session required.

## Naming

The working release name is **Biewer**, after the small Biewer Terrier. As noted in the original proposal, naming and trademark clearance must be repeated before any public release.
