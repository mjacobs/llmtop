# llmtop

An [nvtop](https://github.com/Syllo/nvtop)-like terminal UI for monitoring GPUs
across one or more machines, tailored for LLM inference serving. `llmtop`
charts per-GPU utilization and memory like nvtop, and additionally surfaces
llama.cpp serving stats (model loaded, tokens/sec, KV-cache usage, slot
occupancy) — with a breakdown of GPU usage between LLM serving and everything
else (compositor, browsers, other compute).

*(screenshot coming soon at `docs/demo.png` — run `llmtop --demo` to see it live in the meantime)*

## Install / build

Requires Go 1.26+ and a C toolchain (cgo is used for the NVML bindings):

```bash
CGO_ENABLED=1 go build ./cmd/llmtop
```

This produces a single `llmtop` binary. An NVIDIA driver (`libnvidia-ml.so`)
is only required at **runtime**, not at build time — the binary builds fine
on a machine with no GPU, and gracefully reports "no NVML" on one without a
driver.

## Quick start

```bash
llmtop                 # monitor the local machine (auto-detected GPUs + llama.cpp servers)
llmtop --demo          # synthetic data, no GPU or config required — good for a quick look
llmtop --connect host:9832   # also watch one remote llmtop agent, ad hoc
```

To monitor a remote machine, run the agent there and point a TUI at it:

```bash
# on the remote box
llmtop agent --listen :9832

# on your workstation
llmtop --connect remote-host:9832
```

`--connect` only works against agents running **without** a token — the TUI
has no `--token` flag. To reach an agent that requires one, declare the host
in the config file instead:

```toml
# workstation ~/.config/llmtop/config.toml
[[hosts]]
name  = "remote-host"
addr  = "remote-host:9832"
token = "some-shared-secret"   # must match the agent's token

# remote box (or pass --token to `llmtop agent`)
[agent]
token = "some-shared-secret"
```

The config file is also how you declare hosts (local and/or remote)
permanently — see below.

### Flags

| Command | Flag | Meaning |
| --- | --- | --- |
| `llmtop` | `--config PATH` | config file path (default `~/.config/llmtop/config.toml`) |
| `llmtop` | `--demo` | synthetic data, no GPUs needed |
| `llmtop` | `--connect HOST:PORT` | connect to an agent ad hoc (repeatable) |
| `llmtop agent` | `--config PATH` | config file path |
| `llmtop agent` | `--listen ADDR` | listen address (overrides config, default `:9832`) |
| `llmtop agent` | `--token TOKEN` | bearer token (overrides config) |

## Configuration

`~/.config/llmtop/config.toml` (path overridable with `--config`). No config
file at all is equivalent to monitoring only the local machine with defaults.

```toml
refresh_ms = 1000        # TUI poll + local sample cadence

[[hosts]]                # omit ALL [[hosts]] entries to default to local-only
name  = "local"          # required on every host entry
local = true

  [[hosts.llm_endpoints]] # optional static additions for this (local) host
  url = "http://127.0.0.1:8081"

[[hosts]]
name  = "charlie"
addr  = "192.0.2.x:9832"
token = "…"              # optional, must match agent's

[agent]                   # used by `llmtop agent`
listen = ":9832"
token  = "…"              # optional
```

Each host entry is either `local = true` (an in-process collector) or has an
`addr` (an HTTP client talking to a remote `llmtop agent`) — exactly one of
the two. `llm_endpoints` lets you add LLM server URLs that process-scan
discovery can't see (e.g. containerized servers whose cmdline isn't visible
from `/proc`) — they only apply to `local` hosts, since they're read by
whichever process runs the collector for that host. For a remote host,
declare its `llm_endpoints` in the agent's own config on that machine instead.

## Key bindings

| Key | Action |
| --- | --- |
| `j` / `k` / arrows | select GPU panel |
| `p` | pause updates |
| `+` / `-` | increase / decrease refresh rate |
| `?` | toggle help |
| `q` / `Ctrl-C` | quit |

## llama.cpp notes

- **Discovery** is automatic: `llmtop` scans `/proc` for `llama-server`
  processes and parses `--host`/`--port` from the command line (default port
  8080). Router-mode setups (`--models-dir`) spawn worker children on
  ephemeral ports — those are separate processes and get discovered the same
  way, so both the router and its workers show up.
- **Start llama-server with `--metrics` enabled** to get throughput stats
  (prompt/generation tok/s, KV-cache usage, request queue depth) in `llmtop`.
  Without it, the server still shows up with model/context/slot info and a
  `(--metrics off)` hint instead of throughput numbers — llama.cpp keeps
  running exactly the same either way, `llmtop` just has less to show.
- **Containers / static endpoints:** if a server's process isn't visible from
  the host (e.g. running in a container with a hidden cmdline), add it under
  `[[hosts.llm_endpoints]]` in the config of whichever process collects for
  that machine — the TUI's local host entry when it's the local machine, or
  the agent's own config when it's a remote machine — so `llmtop` polls its
  HTTP API directly even though it can't be discovered via `/proc`.

## Agent security

`llmtop agent` serves read-only JSON metrics over plain HTTP — no TLS in v1.
It's designed for a trusted LAN (e.g. a homelab): point a handful of
workstations at a handful of GPU boxes you already control. An optional
shared bearer token (`--token` / `token =` in config) gates the API if you
want a minimal barrier against other hosts on the same network, but this is
not a substitute for network-level trust. Don't expose the agent's listen
address to an untrusted network without putting it behind a TLS-terminating
reverse proxy or a VPN.
