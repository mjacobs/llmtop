# Working on llmtop

Notes for contributors and coding agents. The README covers usage; this file
covers the invariants that are easy to break.

- **Gate:** `make check` (gofmt, vet, golangci-lint v2, unit tests) must pass.
  `make test-nvml` runs the hardware smoke test and needs a real NVIDIA GPU
  (`LLMTOP_NVML_TEST=1`); everything else runs without one. Use
  `llmtop --demo` (or `make run ARGS=--demo`) to see the TUI without hardware.
- **Layout solver and renderers are in lockstep.** `internal/tui/layout.go`
  decides how many lines each panel and host strip gets by counting, without
  rendering; `panel.go`/`overview.go` must produce exactly that many lines
  (`serverBlockLineCount` ↔ `serverBlockLines`, `hostStripContentLines`
  ↔ `renderHostStrip`, `panelFixedContentLines` ↔ `renderGPUPanel`). Tests
  pin this; if you add or remove a line in a renderer, update the counter and
  the tests together.
- **The layout config is a preference, not a mandate.** `[layout]` reaches
  the solver as `tui.LayoutPref`; `solveLayout` may still shrink the column
  count (`stackedCols`) and must still walk the FULL/CHARTS/COMPACT ladder.
  A slot list only permutes `hostRender.order` — index GPUs through
  `hr.gpuAt(pos)`, never `hr.h.Snap.GPUs[pos]`, or panels and their line
  counts drift apart.
- **Never poll a sleeping llama.cpp server's `/metrics` per tick.** llama.cpp
  counts `/metrics` and `/slots` as activity and they reset the
  `--sleep-idle-seconds` timer (and wake a sleeping server); only `/health` and
  `/props` bypass its task queue. `internal/llm/llamacpp.go` gates `/metrics`
  accordingly — keep that when adding endpoint calls. vLLM's `/metrics` has no
  such side effect and is polled every tick.
- **Throughput is anchor-amortised.** llama.cpp token counters move at request
  completion, so `internal/llm/rate.go` emits rates only when a counter
  changes and shows a decode-counter estimate (`gen ~N t/s`) in between. Its
  tests encode the intended behaviour; read them before changing it.
- **Untrusted strings** (llama.cpp `/props` values, vLLM and NInfer
  `/v1/models`, vLLM metric labels, other processes' cmdlines, remote agent
  payloads) are sanitised in the collector and client
  (`model.Snapshot.Sanitize`); keep new fields covered.
- **Fixtures use reserved names only:** hosts `alpha`/`bravo`/`charlie`,
  addresses in `192.0.2.0/24` or loopback, `example.com` domains.
- Commit messages: imperative subject, wrapped body explaining why; no tool
  attribution trailers.
