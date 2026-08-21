# Biewer

> **Keep your coding agents' processes on a short leash.**

Biewer is a local resource supervisor for Claude Code, OpenAI Codex, and other coding agents. It observes what each agent session spawns, attributes CPU, memory, ports, and process count to that session, flags things like a dev server left running after the agent went idle, and gives you a safe, explicit plan before it kills anything.

Biewer is local-first. It does not proxy model traffic, require an API key, upload transcripts, or depend on a hosted control plane. Live dashboard state and token-source indexes can be stored in embedded RocksDB under `~/.biewer/dashboard.rocksdb`; session history remains plain JSON under `~/.biewer/sessions/` for easy inspection and migration.

**Zero setup to start seeing something.** `biewer enable` auto-discovers Claude Desktop, the ChatGPT desktop app, and bare `claude`/`codex` processes. It also reads recent local transcripts as logical tasks, so project and token attribution do not depend on hooks:

```console
$ biewer enable
Installed local daemon

$ biewer watch
PROJECT          AGENT    SESSION   SRC         STATE     TOKENS  CPU   MEMORY  PIDS  PORTS
my-app           codex    0195d17a  transcript  observed  48.2K   0.0%  0 B     0     -
ChatGPT Desktop  chatgpt  auto-812  auto        active    0       3.1%  680 MiB 9     -
```

Desktop application resources are intentionally kept on a separate shared row. A desktop process can host several tasks, so Biewer does not copy its CPU or memory onto every project. Transcript rows have exact thread/project/token attribution; shared desktop rows have CPU, memory, ports, and the process tree.

Hooks remain optional. Install them only when you want exact CLI PID ownership and Claude Code prompt/tool activity for idle findings:

```console
$ biewer hooks install
Installed Claude and Codex hooks

$ claude   # or: codex
```

In another terminal, Biewer follows the hook-tracked session from outside:

```console
$ biewer watch

PROJECT   AGENT   SESSION   SRC   STATE   CPU    MEMORY   PEAK    PIDS  PORTS
my-app    codex   0195d17a  hook  active  4.2%   612 MiB   1.1 GiB  6    :5173

0195d17a (my-app)
   codex                                                       220 MiB
     npm run dev                                                18 MiB
       node                                                    374 MiB  :5173

  FINDINGS
    WARN  vite remains active 18m after the last tool call (pid 5512, port 5173)
          Attribution: confirmed (hook-owned root + live ancestry)
```

## Status

This is a working MVP, built in one session against the original product proposal below. It covers native-mode tracking end to end: daemon, hook wiring for both agents, pattern-based auto-discovery of Claude Desktop/ChatGPT/bare CLI processes with zero setup, live watch/status, session history, and a confirm-before-kill cleanup flow. **Managed mode (cgroup/VM-enforced ownership) is not implemented** — see [What's not built yet](#whats-not-built-yet). Naming/trademark clearance for public release has not been repeated.

## Install

### Recommended

The generated installer detects macOS/Linux and ARM64/x86-64, verifies the
release archive, installs Biewer under `~/.biewer/bin`, and configures that
directory on `PATH`:

```console
$ curl --proto '=https' --tlsv1.2 -LsSf \
    https://github.com/shaktsin/biewer/releases/latest/download/biewer-installer.sh | sh
$ biewer setup
$ biewer tui
```

Restart the shell, or source the environment file named by the installer,
before running `biewer setup`. Setup is idempotent: it starts the daemon,
installs Claude/Codex hooks, and configures Claude's content-disabled OTLP
metrics. Successful steps are preserved if another step reports a conflicting
existing setting, and rerunning the command is safe.

Release binaries use the dependency-free file dashboard backend so the same
installer works on a clean macOS or Linux machine. RocksDB remains available
as a native source build because its CGO binary requires the matching system
RocksDB library.

### Build from source

Requires Go 1.24+ to build from source. The portable build has no other dependencies and the Go module has zero external `require`s.

```console
$ make build          # builds ./dist/biewer for your current OS/arch
$ make install         # copies it to ~/.local/bin (make sure that's on PATH)
```

For the persistent RocksDB dashboard store, install the native library and
build with the RocksDB tag:

```console
$ brew install rocksdb             # macOS (Debian/Ubuntu: librocksdb-dev)
$ make build-rocksdb               # builds ./dist/biewer with RocksDB
$ make install-rocksdb              # optional: copies it to ~/.local/bin
```

Portable `make build` binaries use atomic state files instead. Both backends
persist the same dashboard data; the TUI header shows `DB ROCKSDB` or `DB FILE`
so the active backend is explicit.

Or cross-compile a portable, file-backed binary for another target (no C toolchain needed):

```console
$ make build-all       # darwin/arm64, darwin/amd64, linux/amd64, linux/arm64
```

## Usage

```console
$ biewer setup                 # recommended first run: daemon + hooks + telemetry
$ biewer enable                # start the local daemon (idempotent)
$ biewer hooks install         # wire up Claude Code hooks + a Codex shell wrapper
$ biewer telemetry install     # send Claude metrics/events to Biewer's OTLP receiver
$ biewer telemetry status      # verify Claude telemetry configuration
$ biewer watch                  # live view, refreshes every 2s, Ctrl-C to exit
$ biewer tui                    # interactive full-screen dashboard (arrows/j/k, r, q)
$ biewer status                  # one-shot snapshot, no loop
$ biewer sessions [--limit N]    # session history (agent, project, duration, state)
$ biewer sessions --events <id>  # the recorded hook events for one session
$ biewer stop <session-id>       # print a kill plan, confirm, then terminate it
$ biewer disable                 # stop the daemon
$ biewer hooks uninstall         # remove the Claude hooks + Codex wrapper again
$ biewer telemetry uninstall     # remove Biewer's Claude telemetry settings
```

`biewer stop` never kills anything without printing the plan first (every attributed pid, its command, and its memory) and asking for confirmation — pass `--yes` to skip the prompt in scripts.

### Dashboard and token accounting

`biewer tui` uses a header + two-pane layout: global totals at the top,
sessions/projects on the left, and resources, token consumption, findings,
and the process tree for the selected session on the right. The daemon writes a
complete dashboard snapshot every scan and the TUI reads that persisted state
through the local Unix-socket API.

Token totals primarily come from local agent JSONL logs. Codex rollout logs
contain the canonical thread ID, cumulative `token_count` events, and session
`cwd`; Claude transcripts provide the corresponding session and message usage
counters. Recent transcripts become logical task rows in the dashboard. An
exact provider session-ID match merges transcript usage into a hook-owned
process row. A wrapper launch ID or OTel `process.pid` plus
`process.creation.time` can also make an exact join. Biewer uses cwd and start
time only when the match is unique, and marks that result `probable`.

Biewer also accepts OTLP/HTTP JSON logs and metrics on loopback. For Claude
Code, run:

```console
$ biewer telemetry install
```

This safely adds signal-specific environment settings to
`~/.claude/settings.json`, refuses to overwrite conflicting exporter settings,
uses Claude's default delta-counter model explicitly, and keeps prompt,
response, tool-detail, tool-content, and raw API body logging disabled. Restart
Claude Code sessions afterward. Biewer captures Claude's per-session token
breakdown, cost, active time, lines added/removed, commits, and pull requests.

For Codex, add this one-line exporter to `~/.codex/config.toml`, then restart
Codex:

```toml
[otel]
log_user_prompt = false
exporter = { otlp-http = { endpoint = "http://127.0.0.1:4318/v1/logs", protocol = "json" } }
```

The OTLP receiver retains only provider/session identifiers, Biewer's optional
launch correlation ID, validated process identity (`process.pid` plus
`process.creation.time`) when supplied, delivery-deduplication IDs,
model/workspace metadata, event time, token counters, and aggregate operational
metrics. Log bodies, prompts, responses, tool arguments, and tool output are ignored. Local transcripts remain the
authoritative cumulative usage and project source when both inputs exist. Some
desktop builds may not emit configured OTLP events, so desktop attribution does
not depend on telemetry being available.

### Configuration

Finding thresholds are overridable via environment variables, read by the daemon at `biewer enable` time (set them before running `enable`, since they're picked up by the spawned daemon process):

- `BIEWER_IDLE_THRESHOLD` — a Go duration (e.g. `5m`, `30s`) after the last recorded prompt/tool activity before a still-listening dev-server-looking process is flagged. Default `10m`.
- `BIEWER_MEMORY_THRESHOLD_MB` — attributed memory (in MiB) above which a session gets an informational memory-footprint finding. Default `2048`.
- `BIEWER_DISABLE_AUTO_DISCOVER` — set to any non-empty value to turn off process-pattern auto-discovery. Transcript/telemetry logical tasks and hook/wrapper sessions remain visible.
- `BIEWER_OTLP_ADDR` — loopback address for Claude/Codex OTLP/HTTP JSON logs and metrics. Default `127.0.0.1:4318`; set to `off` to disable. Non-loopback addresses are rejected.

## How attribution works

Biewer never proxies or intercepts your agent's traffic. It maintains two deliberately separate kinds of dashboard rows:

- **Logical tasks**: local transcripts, optional Claude/Codex OTLP data, or exact hook session IDs provide project, activity, model, token, and provider-metric data. These rows do not claim CPU/memory unless an exact process-owned session ID exists.
- **Process resources**: hooks/wrappers or process auto-discovery provide CPU, memory, ports, findings, and process trees. Desktop application rows are marked shared because one Electron process tree can contain multiple logical tasks.

It learns process ownership three ways:

- **Claude Code**: native hooks (`SessionStart`, `UserPromptSubmit`, `PreToolUse`, `SessionEnd`) wired into `~/.claude/settings.json`. `SessionStart` also captures `$PPID`, i.e. Claude Code's own process ID, giving Biewer an exact root PID with no guessing.
- **Codex**: a shell function wrapper installed into your `~/.zshrc` (or `~/.bashrc`/`~/.bash_profile`) that generates an unguessable `biewer.launch.id`, passes it through `OTEL_RESOURCE_ATTRIBUTES`, backgrounds the real `codex` binary, captures its PID via `$!`, and reports session start/end around it. When Codex returns that launch attribute in OTLP, Biewer joins the canonical `conversation.id` to the exact process tree. Codex does not currently get the same per-tool activity granularity Claude Code does — see below.
- **Auto-discovery** (`internal/discover`, `SRC auto` in `watch`/`status`): with no hooks at all, the daemon pattern-matches the running process table each scan tick for Claude Desktop, the ChatGPT desktop app, or a bare `claude`/`codex` binary, and takes the top-level matched process (not any of its own helper/child processes) as the root. This is what makes `biewer enable` alone show something immediately. The tradeoff: there's no session_start event, so `StartedAt` is just "first time Biewer's scanner happened to see it," and there's no per-tool activity signal, so idle-based findings (the WARN below) never fire for an auto-discovered session — only the memory-footprint INFO finding does, since that doesn't depend on activity timing. If a hook or the Codex wrapper already announced a pid as a real session, auto-discovery explicitly excludes it, so nothing is ever double-listed.

From a session's root PID (whichever way Biewer learned it), the daemon rescans the OS process table roughly every 2 seconds and walks live parent→child links to find everything still attributed to that session — this is "confirmed" attribution in the project's original evidence-hierarchy design. Listening TCP ports are matched to attributed processes via `lsof` (native mode) so a lingering `vite`/`next dev`/etc. can be named specifically in findings.

Dashboard rows expose the evidence level: `confirmed` for canonical hook IDs,
launch-ID joins, or matching OTel PID/creation-time identity; `probable` only
for a unique provider + cwd + near-start-time match; `shared` for multiplexed
desktop process trees; and `none` for logical rows with no trustworthy process
owner. Ambiguous probable matches remain separate rather than guessing.

Idle detection (hook-tracked sessions only) compares "now" against the session's last recorded hook activity (a prompt submitted or a tool used), not just whether the agent binary is still running — that's what lets Biewer distinguish "the agent is still actively working" from "the agent process is alive but idle, and it left a dev server behind."

## What's not built yet

Being direct about the gaps rather than letting the demo output imply more than what's here:

- **Managed mode** (`biewer attach ... --install-guest`, cgroup/VM-enforced containment) from the original proposal is not implemented. Everything here is native mode only.
- **Auto-discovery patterns are best-effort strings**, not verified against a real running Claude Desktop or ChatGPT desktop app (this was built without one available to inspect) — see `internal/discover/patterns.go`. If a real install reports a different executable path than `<App>.app/Contents/MacOS/<App>`, the pattern for that app simply won't match and it won't show up in `watch`; add/adjust a `Pattern` there and it'll pick it up on the next scan tick, no restart needed beyond `biewer enable`.
- **Auto-discovered sessions aren't persisted** to `biewer sessions` history — no session_start/session_end event exists to anchor a real lifecycle record, so they only exist in the live `watch`/`status` view while their process is running.
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
internal/discover/      pattern-based auto-discovery (Claude Desktop, ChatGPT, bare claude/codex) — no hooks needed
internal/findings/      pure, unit-tested rules (idle dev server, high memory)
internal/db/            session history plus RocksDB/file-backed dashboard state
internal/daemon/        the supervisor: HTTP API over a Unix socket, scan/attribution loop, kill plans
internal/cli/           `biewer` subcommands, hook installers, table/tree rendering
```

## Development

```console
$ make test    # go test ./...
$ make vet     # go vet ./...
$ make fmt     # gofmt -l -w .
```

### Releases

Release packaging uses cargo-dist's generic-project support. The package
metadata is in `dist.toml`; `dist-workspace.toml` defines CI and hosting; and
`scripts/dist-build.sh` maps cargo-dist target triples to `GOOS`/`GOARCH`.
The generated `.github/workflows/release.yml` must be regenerated rather than
edited manually.

```console
$ curl --proto '=https' --tlsv1.2 -LsSf \
    https://github.com/axodotdev/cargo-dist/releases/download/v0.32.0/cargo-dist-installer.sh | sh
$ dist plan                    # validate config and preview artifacts
$ dist generate                # regenerate release.yml after config changes
$ git tag v0.1.0-mvp           # must match the version in dist.toml
$ git push origin v0.1.0-mvp   # publishes archives + biewer-installer.sh
```

The process scanner and findings engine are platform-agnostic-by-design and unit tested against fake process tables (`internal/procscan/linux_test.go` builds a fake `/proc`, `internal/daemon/daemon_test.go` injects a fake scanner) rather than depending on real OS processes, so `go test ./...` is fast and deterministic in CI or a sandboxed container — no root, no real agent session required.

## Naming

The working release name is **Biewer**, after the small Biewer Terrier. As noted in the original proposal, naming and trademark clearance must be repeated before any public release.
