# Recorded engine responses

The tests serve these responses from `httptest`. They run without a GPU, a model
or access to an inference server.

- `llama.cpp-b11541-ce8c04021` was captured from an isolated llama.cpp server
  with one slot and a 512-token context. `/health` and `/metrics` are the
  captured bodies. `/props` retains the fields the adapter reads plus the
  build ID. The model alias and path use fixture names in `/props` and the
  recorded `/v1/models` response.
- `vllm-0.18.0` was captured from vLLM 0.18.0 with CUDA 12.9, one sequence, and
  a 512-token context. The public Qwen2.5-0.5B-Instruct checkpoint was served as
  `alpha`. The model list replaces the checkpoint name with `alpha`. The empty
  health body represents a successful HTTP 200 response. The metrics recording
  has trailing line whitespace removed.

Both metrics responses follow a disposable request with one prompt token and
two generated tokens. The stored fixtures are inputs to the adapters, so keep
the server version in the directory name and use a new directory for a new
version.

## Record a test server

Run from the repository root with Python 3. Use a disposable server or an
endpoint approved for testing. For example:

```sh
python3 scripts/record-engine-fixtures.py vllm 0.18.0 http://127.0.0.1:8000 \
  --out "$(mktemp -d)"
```

The recorder fetches only the endpoints for the selected engine. It refuses
to overwrite an existing fixture directory and checks the response bodies
for private paths, addresses and credential-shaped values. `--redactions`
takes a private JSON file mapping exact original strings to safe replacements.
`--denylist` supplies one private term per line. The recorder also checks
`PRIVATE_TERMS_FILE` or the default user denylist if one exists. Review the
saved files for any remaining private model or host names before committing.
`LLMTOP_RECORD_TOKEN` can supply a bearer token for the chosen origin.

By default, llama.cpp recording calls only `/health` and `/props`. Use
`--allow-llama-activity` to include `/metrics` and `/v1/models` on a server you
are willing to wake. A scrape of `/metrics` can wake a sleeping llama.cpp
server and reset its idle timer.

## Check the live adapter

```sh
LLMTOP_LIVE_KIND=vllm LLMTOP_LIVE_ENDPOINT=http://127.0.0.1:8000 make test-llm-live
```

The test calls the selected adapter once. The normal test gate skips it. For
llama.cpp, also set `LLMTOP_LIVE_ALLOW_LLAMA_ACTIVITY=1` only on a server you
are willing to wake. `LLMTOP_LIVE_TOKEN` supplies an optional key to adapters
that support one.
