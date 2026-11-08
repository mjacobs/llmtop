#!/usr/bin/env python3
"""Record adapter inputs from an explicitly selected test server."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import sys
import urllib.error
import urllib.parse
import urllib.request

ENDPOINTS = {
    "llama.cpp": {"/health": "health.json", "/props": "props.json"},
    "vllm": {"/health": "health.json", "/v1/models": "models.json", "/metrics": "metrics.prom"},
    "ninfer": {"/health": "health.json", "/v1/models": "models.json"},
    "strata": {"/health": "health.json", "/v1/models": "models.json", "/metrics": "metrics.json"},
}
MAX_BODY = 1 << 20
PRIVATE_SHAPE = re.compile(
    r"(?:/(?:home|Users|mnt|srv|opt)/|\b(?:10\.\d{1,3}|192\.168|172\.(?:1[6-9]|2\d|3[01])|169\.254)\.\d{1,3}\.\d{1,3}\b|"
    r"\.ts\.net\b|\.local\b|\bBearer\s+\S+|"
    r'\b(?:api[_-]?key|authorization|access[_-]?token)"?\s*[:=]\s*"[^"]+)',
    re.IGNORECASE,
)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def capture(origin, endpoints, token, redactions, private_terms):
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    bodies = {}
    for path, filename in endpoints.items():
        request = urllib.request.Request(origin + path, headers=headers)
        try:
            with client.open(request, timeout=10) as response:
                data = response.read(MAX_BODY + 1)
        except urllib.error.HTTPError as exc:
            if path != "/health" and exc.code in (404, 501):
                continue
            raise ValueError(f"HTTP {exc.code} at {path}") from None
        except urllib.error.URLError:
            raise ValueError(f"request failed at {path}") from None
        if len(data) > MAX_BODY:
            raise ValueError(f"response at {path} exceeds 1 MiB")
        body = data.decode("utf-8")
        for old, new in redactions:
            body = body.replace(old, new)
        folded = body.casefold()
        if (token and token in body) or PRIVATE_SHAPE.search(body) or any(term in folded for term in private_terms):
            raise ValueError(f"possible private data at {path}; redact it and record again")
        if filename.endswith(".json") and (body.strip() or path != "/health"):
            try:
                json.loads(body)
            except ValueError:
                raise ValueError(f"invalid JSON at {path}") from None
        bodies[filename] = body
    return bodies


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=ENDPOINTS)
    parser.add_argument("version", help="exact reported engine version or build ID")
    parser.add_argument("endpoint", help="HTTP origin of a disposable or approved test server")
    parser.add_argument("--out", type=Path, default=Path("internal/llm/testdata/engines"))
    parser.add_argument("--redactions", type=Path, help="private JSON mapping from original text to safe fixture text")
    parser.add_argument("--denylist", type=Path, help="private terms file, one term per line")
    parser.add_argument("--allow-llama-activity", action="store_true", help="allow llama.cpp metrics and model requests, which can wake a server")
    args = parser.parse_args()
    try:
        origin = urllib.parse.urlsplit(args.endpoint)
        if (
            origin.scheme not in ("http", "https") or not origin.hostname
            or origin.username is not None or origin.password is not None
            or origin.path not in ("", "/") or origin.query or origin.fragment
        ):
            raise ValueError("endpoint must be an HTTP origin without credentials, path, query or fragment")
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,79}", args.version):
            raise ValueError("invalid engine version")
        destination = args.out / f"{args.kind}-{args.version}"
        if destination.exists():
            raise ValueError("fixture directory already exists; choose a fresh output directory")
        redactions = {}
        if args.redactions:
            redactions = json.loads(args.redactions.read_text())
            if not isinstance(redactions, dict) or any(not key or not isinstance(value, str) for key, value in redactions.items()):
                raise ValueError("redactions must map nonempty strings to strings")
        denylist = args.denylist or Path(os.environ.get("PRIVATE_TERMS_FILE") or Path.home() / ".config/private-terms.txt")
        terms = []
        if args.denylist or denylist.is_file():
            terms = [line.strip().casefold() for line in denylist.read_text().splitlines() if line.strip() and not line.lstrip().startswith("#")]
        endpoints = dict(ENDPOINTS[args.kind])
        if args.kind == "llama.cpp" and args.allow_llama_activity:
            endpoints.update({"/metrics": "metrics.prom", "/v1/models": "models.json"})
        token = os.environ.get("LLMTOP_RECORD_TOKEN", "")
        if any(not 32 < ord(char) < 127 for char in token):
            raise ValueError("LLMTOP_RECORD_TOKEN must contain only visible ASCII")
        bodies = capture(
            f"{origin.scheme}://{origin.netloc}", endpoints, token,
            sorted(redactions.items(), key=lambda item: -len(item[0])), terms,
        )
        args.out.mkdir(parents=True, exist_ok=True)
        destination.mkdir(mode=0o700)
        try:
            for name, body in bodies.items():
                (destination / name).write_text(body, encoding="utf-8")
        except OSError:
            shutil.rmtree(destination)
            raise
    except (OSError, ValueError) as exc:
        parser.exit(1, f"recording refused: {exc}\n")
    print(f"Recorded {len(bodies)} responses for {args.kind} {args.version}. Inspect all files before committing.")


if __name__ == "__main__":
    main()
