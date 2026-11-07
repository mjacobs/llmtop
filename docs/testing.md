# Testing strategy

llmtop's unit tests are strong: 230 tests, 87.6% statement coverage, and a
gate that runs in under ten seconds. What they miss are inputs nobody wrote a
fixture for, the JSON contract between agents and TUIs of different versions,
and the thin glue code that wires the pieces together. This document says what
each kind of test is for, where the gaps are, and the order to close them in.

The first section (layers, conventions, gates) is meant to stay true. The
"State on 2026-11-06" and "Work plan" sections are a dated snapshot; update or
delete them as the work lands.

## What must never break

These are the properties a user would notice, in rough order of harm. Every
test should trace back to one of them.

| Property | Why it matters | Guarded today by |
| --- | --- | --- |
| A sleeping llama.cpp server is never woken by polling | `/metrics` and `/slots` reset `--sleep-idle-seconds`, so llmtop would keep models resident and burn VRAM | 12 sleep-gate tests in `internal/llm/llamacpp_test.go` |
| Untrusted strings cannot put escape sequences on the terminal | Model names, process names and remote agent payloads go straight to the screen | `model.Snapshot.Sanitize` tests; collector and client sanitise tests |
| Hidden addresses stay hidden | Screenshots and screen shares are the reason the option exists | Exhaustive scrub tests in `internal/tui/view_test.go` |
| The frame never exceeds the terminal | Overflow scrolls the alt screen and garbles the display | Fixed-size fit tests in `overview_test.go` and `app_test.go` |
| Live data keeps flowing when one part fails | A dead GPU driver, a failed backfill or one unreachable server must not blank the host | Collector failure tests; client backfill-wedge test |
| The agent rejects requests without the token | The agent exposes process and model telemetry | `internal/agent/agent_test.go` `TestAuth` |
| An older agent and a newer TUI (or the reverse) still talk | Hosts are upgraded one at a time | Snapshot JSON golden and frozen-release decoder tests in `internal/model/wire_test.go` |
| Throughput numbers mean what the README says | llama.cpp counters move only at request completion | `internal/llm/rate_test.go` |

## Test layers

The pyramid still applies, with two extra layers between unit and system:
property tests for inputs from outside the process, and contract tests for
data that crosses a version boundary.

1. **Unit tests (most tests).** Pure functions with table-driven cases:
   parsers, the rate tracker, the layout solver, the address scrubber, config
   validation. They take explicit inputs, including `now`, and never sleep.
2. **Component tests with fakes.** One package against fakes of its
   neighbours: engine adapters against `httptest` servers; the collector with
   `gpu.NewFake` and `procs.Fake`; the TUI `App` driven through `Update` and
   `render` without a terminal; the client against a real `agent.Handler`.
3. **Property and fuzz tests.** Go native fuzz targets (`testing.F`) for every
   parser of outside input, and for the renderer's size guarantees. Their seed
   corpus runs as ordinary tests in every `go test`, so they cost nothing in
   the gate; longer fuzzing runs are opt-in.
4. **Contract tests.** Golden files under `testdata/` for anything another
   version or another program reads or writes: the agent's JSON snapshot, and
   real response bodies captured from each inference engine.
5. **System smoke tests (fewest).** The built binary or real hardware:
   `make test-nvml` on an NVIDIA host, `llmtop --demo` in a fixed-size `tmux`
   pane, and an opt-in check against a live inference server. These run before
   a release, not on every commit.

### What not to test

- **Full ANSI frame snapshots.** Lip Gloss upgrades change escape output, and
  the charm stack is bumped often. Assert structure instead: line count, line
  width (`lipgloss.Width`), and the substrings that carry meaning.
- **Bubble Tea's program loop.** Test `App.Update` and `App.render` directly,
  as `app_test.go` does. `tui.Run` and `main` should stay thin enough that the
  smoke test covers them.
- **Coverage for its own sake.** Don't mock the world to reach `main()` or
  `tui.Run`; extract the logic worth testing and leave the wiring to the
  smoke test.

## Conventions

- Tests live next to the code, in the same package.
- Fixtures use reserved names only: hosts `alpha`, `bravo`, `charlie`;
  addresses in `192.0.2.0/24` or loopback; `example.com` domains.
- When a renderer gains or loses a line, update its line counter and the
  lockstep test in the same change (`serverBlockLineCount` ↔
  `serverBlockLines`, `hostStripContentLines` ↔ `renderHostStrip`,
  `panelFixedContentLines` ↔ `renderGPUPanel`).
- Time: pass `now` into the code (as `RateTracker.TPS` does) or run the test
  inside `testing/synctest`. Avoid real sleeps longer than a few
  milliseconds. Code tested under `synctest` must not use real sockets (a
  goroutine blocked on network I/O stops the fake clock), so give it an
  `http.Client` whose transport calls the handler in process.
- Hardware and live-service tests are opt-in through an environment variable
  named `LLMTOP_<THING>_TEST`, skip otherwise, and have a `make` target.
- Any `/metrics` request wakes a sleeping llama.cpp server and resets its idle
  timer. Only point an opt-in live test at a server you are willing to wake.
- Every bug fix adds a test that fails without the fix.

## Gates

| When | What runs | Where |
| --- | --- | --- |
| Every change | `make check`: gofmt, `go vet`, golangci-lint, `go test ./...` (includes fuzz seed corpora) | local and CI, Go 1.26 floor and stable |
| Every change | `make check-nocgo`: build and test without cgo | CI |
| Every change (to add) | `go test -race ./...` | CI |
| After touching an outside-input parser | `make fuzz` (30 s per target; override with `FUZZTIME=60s`, two workers) | local |
| Before a release | `make test-nvml`; the `--demo` tmux smoke test; the live engine check for each engine available; review any golden file diff | a host with an NVIDIA GPU |

## State on 2026-11-06

Measured at commit `b20219f` with Go 1.27.3:

- `make check` passes in 8.5 s wall time. `go test -race ./...` also passes,
  but CI doesn't run it.
- `make test-nvml` passes on a two-GPU host.
- 230 test functions, 1 benchmark, 0 fuzz targets, no `testdata/` directory.
- The client package takes 7.1 s, and 7.0 s of that is one test
  (`TestStartRemoteBackfillWedgeFallsBackToLive`) sleeping through real
  backoff delays of 1, 2 and 4 seconds. It is the slowest part of
  `go test ./...`.

Statement coverage by package (`go test -coverprofile`):

| Package | Coverage | What is uncovered |
| --- | --- | --- |
| `cmd/llmtop` | 0.0% | All of it: subcommand dispatch, flag parsing, token precedence, the loopback check |
| `internal/gpu` | 58.4% | `nvmlVendor.Sample` (6.9%), which needs a driver; the device-process merge is tested with a fake device |
| `internal/agent` | 66.7% | `RunSampling` and `Run`: the sampling loop, listen errors, shutdown |
| `internal/client` | 92.0% | Cancellation paths in `send` and `StartLocal` |
| `internal/tui` | 93.0% | `Run`, `startSources`, `App.View`, `App.Init` |
| `internal/procs` | 92.6% | Malformed `/proc/<pid>/stat` lines in `parseStat` |
| `internal/config` | 93.9% | |
| `internal/history` | 94.4% | |
| `internal/llm` | 94.5% | Label edge cases in `parsePromLabels` (68.6%) |
| `internal/collector` | 95.6% | |
| `internal/model` | 100.0% | |
| **Total** | **87.6%** | |

The commit history shows where untested shapes have hurt before:

- Three fixes kept the frame inside the terminal height (`b732137`,
  `a095125`, `25c2e70`). The fit tests check fixed lists of sizes, so each
  fix added the size that had failed.
- Four fixes changed how listen addresses are parsed from another process's
  command line (`7b19fe9`, `7e660e4`, `41a00b1`, `5521598`).
- Two fixes landed in `cmd/llmtop/main.go`, which has no tests: an explicit
  empty `--token=` must turn auth off (`1da82ac`), and stray arguments after
  `version` must be rejected (`94ece4f`).

### A bug found while writing this

The Prometheus parser accepts `NaN` and `±Inf`, which are legal sample values,
and nothing downstream filters them. Go's JSON encoder refuses non-finite
floats. The agent writes its 200 status before encoding, so one snapshot
holding a NaN metric makes `/api/v1/history` answer HTTP 200 with an empty
body until 300 newer samples have been taken (about five minutes at the
default one-second refresh). A TUI that reconnects during that window gets no
history. A throwaway test confirmed this. By the same code path, a server that
keeps reporting NaN would make `/api/v1/snapshot` fail every tick, and the
host would show as erroring. No engine has been seen emitting NaN; the gap is
that nothing would stop it.

## Work plan

In priority order. Each item is one change with its own tests. Sizes are
rough: S is under an hour, M is a few hours, L is a day or more.

1. **Stop non-finite metrics from breaking the agent (S, bug).** Replace NaN
   and ±Inf with "unknown" where adapters build `LLMMetrics`, or in
   `Snapshot.Sanitize`, and make `writeJSON` encode into a buffer so an encode
   failure becomes HTTP 500 rather than an empty 200. Tests: an adapter fed
   `NaN` yields a finite value; the agent's history stays decodable with a
   non-finite value in the ring.
2. **Run the race detector in CI (S).** Add a `test-race` target and a CI step.
   The collector fetches every server concurrently and the agent serves
   requests while sampling, so data races are a real risk. It passes today.
3. **Fuzz the parsers of outside input (M).** Targets for
   `ParsePromSamples`, the four `*EndpointFromCmdline` functions, and
   `parseStat`. Seed each from the existing table cases. Properties: never
   panics; every returned endpoint parses as an `http://host:port` origin with
   a valid port, a loopback or local host, and no path, query or user info;
   every returned metric sample has a name. Add `make fuzz FUZZTIME=30s`.
4. **Fuzz the renderer's size guarantees (M).** One target that builds hosts
   from fuzzed counts of GPUs and servers and fuzzed (sanitised) names, then
   renders at a fuzzed size and layout mode. Properties: line count at most
   the height, every line at most the width, and each line counter matching
   its renderer. This is the test that would have caught the three height
   fixes.
5. **Pin the wire format (done, before tagging v0.1.0).** A golden
   `internal/model/testdata/wire/snapshot.json`, regenerated only with
   `-update`, so renaming a JSON field shows up as a diff in review. At each
   release, freeze a copy as `v<version>.json` and keep a test that decodes
   every frozen copy with the current types. See the
   [release checklist](releases/README.md) for the freeze procedure.
6. **Make `Sanitize` coverage automatic (S).** A reflection test that sets
   every string field in a `Snapshot` to a value with control characters,
   calls `Sanitize`, and fails if any control character survives. A new string
   field that nobody added to `Sanitize` then fails the gate instead of
   relying on review.
7. **Replace real-time waits with `synctest` (M).** Give `StartRemote` an
   unexported variant that takes an `*http.Client`, then test the backoff
   schedule exactly (1 s, 2 s, 4 s … capped at 30 s), the fallback after three
   failed backfills, and that the goroutine exits on cancel. Do the same for
   `agent.RunSampling` and `client.StartLocal`. Removes the 7 s wait.
8. **Test the CLI (M).** Move flag handling out of `main` and `runAgent` into
   functions that take `args` and a `getenv` function and return a config or
   an error. Table-test subcommand dispatch, stray arguments, token precedence
   (flag, then `LLMTOP_TOKEN`, then config; an explicit empty flag turns auth
   off), the missing-explicit-`--config` error, and `isLoopbackAddr`.
9. **Test `nvmlVendor.Sample` without a GPU (M).** Let `nvmlVendor` hold an
   `nvml.Interface` and test `Sample` against go-nvml's mock servers
   (`pkg/nvml/mock/dgxa100`): device count, milliwatt-to-watt conversion,
   a device whose handle lookup fails, and fields that fail individually.
   `make test-nvml` stays as the real-driver check.
10. **Record real engine responses (M).** A script that saves `/props`,
    `/metrics`, `/health` and `/v1/models` bodies from a running server into
    `internal/llm/testdata/engines/<engine>-<version>/`, with the engine
    version in the directory name. Adapter tests then run against real
    payloads as well as the hand-written ones, which matters most for vLLM and
    NInfer: neither has been exercised live. Pair it with an opt-in
    `LLMTOP_LIVE_TEST=1` check that runs one adapter `Fetch` against an
    endpoint you name.
11. **Smoke-test the binary (S).** A script that runs `llmtop --demo` in a
    detached `tmux` pane at 80×24 and 200×50, captures the pane, checks the
    header rendered and no Go panic was printed, sends `q`, and checks the
    exit status. It reuses the approach in `scripts/demo-screenshot.sh` and
    covers `tui.Run` and `main`.

### Coverage targets

Hold each package at a floor instead of chasing one total:

| Package | Now | Target | Reached by |
| --- | --- | --- | --- |
| `cmd/llmtop` | 0% | 70% | item 8 |
| `internal/agent` | 67% | 90% | items 1 and 7 |
| `internal/gpu` (cgo build) | 58% | 85% | item 9 |
| Every other package | 92–100% | no lower than today, rounded down to the nearest 5% | review |

## Example test cases

Sketches for the work plan above. Names and helpers are illustrative.

Renderer fit (item 4):

```go
func FuzzRenderOverviewFits(f *testing.F) {
	f.Add(uint16(80), uint16(24), uint8(2), uint8(1), "qwen36-35b-mtp", uint8(0))
	f.Add(uint16(5), uint16(3), uint8(0), uint8(0), "", uint8(3))
	f.Fuzz(func(t *testing.T, w, h uint16, nGPU, nSrv uint8, name string, mode uint8) {
		width, height := int(w%400)+1, int(h%200)+1
		hosts := fuzzHosts(int(nGPU%8), int(nSrv%4), model.CleanString(name))
		prefs := []LayoutPref{{}, {Mode: LayoutOneRow}, {Mode: LayoutStackByHost}, {Mode: LayoutColumns, Columns: 3}}
		out := RenderOverview(hosts, 0, width, height, prefs[int(mode)%len(prefs)], DefaultTheme())
		if out == "" {
			return
		}
		lines := strings.Split(out, "\n")
		if len(lines) > height {
			t.Fatalf("%dx%d: %d lines", width, height, len(lines))
		}
		for _, l := range lines {
			if lipgloss.Width(l) > width {
				t.Fatalf("%dx%d: line width %d: %q", width, height, lipgloss.Width(l), l)
			}
		}
	})
}
```

Discovered endpoints are always a local origin (item 3):

```go
func FuzzEndpointFromCmdline(f *testing.F) {
	f.Add("llama-server\x00--host\x000.0.0.0\x00--port\x008080")
	f.Add("python3\x00-m\x00serve.server\x00--engine\x00strata")
	orig := isLocalIP
	isLocalIP = func(net.IP) bool { return false } // deterministic: only loopback counts
	f.Cleanup(func() { isLocalIP = orig })
	f.Fuzz(func(t *testing.T, joined string) {
		argv := strings.Split(joined, "\x00")
		for _, parse := range []func([]string) (string, bool){
			EndpointFromCmdline, VLLMEndpointFromCmdline, NInferEndpointFromCmdline, StrataEndpointFromCmdline,
		} {
			ep, ok := parse(argv)
			if !ok {
				continue
			}
			u, err := url.Parse(ep)
			if err != nil || u.Scheme != "http" || u.Path != "" || u.RawQuery != "" || u.User != nil {
				t.Fatalf("%q -> %q", argv, ep)
			}
			if ip := net.ParseIP(u.Hostname()); u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
				t.Fatalf("%q -> non-local host %q", argv, ep)
			}
		}
	})
}
```

Non-finite metrics (item 1):

```go
func TestHistoryStaysDecodableWithNonFiniteMetric(t *testing.T) {
	s, ts := newTestServer(t, "")
	s.SampleNow(context.Background())
	snap, _ := s.ring.Last()
	snap.Servers = []model.LLMServer{{Kind: "llama.cpp", Endpoint: "http://127.0.0.1:8080",
		Status: model.StatusOK, Metrics: &model.LLMMetrics{KVCacheUsage: math.NaN()}}}
	s.ring.Push(snap)

	resp, body := get(t, ts.URL+"/api/v1/history", "")
	var hist []model.Snapshot
	if err := json.Unmarshal(body, &hist); resp.StatusCode == http.StatusOK && err != nil {
		t.Fatalf("HTTP 200 with an undecodable body (%d bytes): %v", len(body), err)
	}
}
```

Backoff schedule on a fake clock (item 7):

```go
func TestStartRemoteBackoffSchedule(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		attempts := make(chan time.Duration, 16)
		hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts <- time.Since(start)
			return nil, errors.New("connection refused")
		})}
		ch := make(chan Update, 64)
		startRemote(t.Context(), "alpha", "192.0.2.10:9832", "", time.Second, ch, hc)
		// Waits of 1, 2, 4, 8, 16 s, then capped at 30 s. Receiving blocks
		// durably, so the fake clock jumps straight to each attempt.
		for i, w := range []time.Duration{0, 1, 3, 7, 15, 31, 61, 91} {
			if got := <-attempts; got != w*time.Second {
				t.Fatalf("attempt %d at %v, want %v", i, got, w*time.Second)
			}
		}
	})
}
```

Token precedence (item 8):

```go
func TestAgentTokenPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		env, want string
	}{
		{"flag wins", []string{"--token=a"}, "b", "a"},
		{"explicit empty flag turns auth off", []string{"--token="}, "b", ""},
		{"environment beats config", nil, "b", "b"},
		{"config is the fallback", nil, "", "from-config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := writeConfig(t, `[agent]`+"\n"+`token = "from-config"`)
			args := append([]string{"--config", cfgPath}, tc.args...)
			cfg, err := agentConfig(args, func(string) string { return tc.env })
			if err != nil || cfg.Agent.Token != tc.want {
				t.Fatalf("token = %q, err %v; want %q", cfg.Agent.Token, err, tc.want)
			}
		})
	}
}
```
