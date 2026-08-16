#!/usr/bin/env bash
# Regenerate docs/demo.png from `llmtop --demo`.
#
# Runs the real TUI in a detached tmux pane at a fixed size, lets the demo
# feed build up some chart history, captures the pane with its ANSI colours,
# and renders that with freeze (https://github.com/charmbracelet/freeze).
# ImageMagick + optipng shrink the result to something README-sized.
#
# Maintainer tool only — needs: tmux, freeze, magick, optipng.
#
#   make build && scripts/demo-screenshot.sh            # writes docs/demo.png
#   COLS=160 ROWS=48 SECS=90 scripts/demo-screenshot.sh  # bigger, longer history
set -euo pipefail

cd "$(dirname "$0")/.."

for tool in tmux freeze magick optipng; do
	command -v "$tool" >/dev/null || { echo "missing: $tool" >&2; exit 1; }
done
[[ -x ./llmtop ]] || { echo "./llmtop not built — run make build first" >&2; exit 1; }

COLS=${COLS:-140}
ROWS=${ROWS:-40}
SECS=${SECS:-75}     # seconds of demo history to accumulate at 0.5s refresh
OUT=${OUT:-docs/demo.png}
WIDTH=${WIDTH:-2400} # final image width in px

session="llmtop-shot-$$"
tmp=$(mktemp -d)
trap 'tmux kill-session -t "$session" 2>/dev/null || true; rm -rf "$tmp"' EXIT

tmux new-session -d -s "$session" -x "$COLS" -y "$ROWS" \
	"TERM=xterm-256color COLORTERM=truecolor ./llmtop --demo"
sleep 2
tmux send-keys -t "$session" '+' # halve the refresh interval so charts fill faster
sleep "$SECS"
tmux capture-pane -t "$session" -e -p >"$tmp/demo.ansi"
tmux kill-session -t "$session"

freeze "$tmp/demo.ansi" --language ansi -o "$tmp/demo.png" \
	--window --background '#1e1e2e' --padding 20,24 --margin 0 \
	--font.size 13 --border.radius 8 >/dev/null

mkdir -p "$(dirname "$OUT")"
magick "$tmp/demo.png" -filter Lanczos -resize "${WIDTH}x" -strip \
	-dither None -colors 256 "PNG8:$OUT"
optipng -quiet -o3 "$OUT"
echo "wrote $OUT ($(du -h "$OUT" | cut -f1))"
