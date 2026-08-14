#!/usr/bin/env bash
# Launch TradingView Desktop with a loopback-only Chrome DevTools Protocol port.
# One debug-enabled TradingView instance can be shared by Watchlist Tool,
# DaiDai, and other local applications.
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "This launcher is for macOS." >&2
  exit 69
fi

for command in curl osascript pgrep pkill; do
  command -v "$command" >/dev/null 2>&1 || {
    echo "Missing required macOS command: $command" >&2
    exit 69
  }
done

port="${1:-9222}"
if ! [[ "$port" =~ ^[0-9]+$ ]] || (( port < 1 || port > 65535 )); then
  echo "Invalid port: $port" >&2
  exit 64
fi

endpoint="http://127.0.0.1:$port"
log_dir="${WATCHLIST_LOG_DIR:-$HOME/.watchlist-tool}"
log_file="$log_dir/tradingview-debug.log"
mkdir -p "$log_dir"

cdp_ready() {
  curl --noproxy '*' --silent --fail --max-time 1 "$endpoint/json/version" >/dev/null 2>&1
}

chart_ready() {
  local targets
  targets="$(curl --noproxy '*' --silent --fail --max-time 1 "$endpoint/json/list" 2>/dev/null || true)"
  grep -qi 'tradingview' <<<"$targets"
}

if cdp_ready && chart_ready; then
  echo "TradingView CDP is already ready at $endpoint"
  echo "The existing instance can be shared by all local tools."
  exit 0
fi

bundle=""
for candidate in "/Applications/TradingView.app" "$HOME/Applications/TradingView.app"; do
  if [[ -x "$candidate/Contents/MacOS/TradingView" ]]; then
    bundle="$candidate"
    break
  fi
done

if [[ -z "$bundle" ]] && command -v mdfind >/dev/null 2>&1; then
  for bundle_id in com.niceincontact.TradingView com.tradingview.tradingview; do
    candidate="$(mdfind "kMDItemCFBundleIdentifier == '$bundle_id'" 2>/dev/null | head -n 1 || true)"
    if [[ -n "$candidate" && -x "$candidate/Contents/MacOS/TradingView" ]]; then
      bundle="$candidate"
      break
    fi
  done
fi

if [[ -z "$bundle" ]]; then
  candidate="$(find /Applications "$HOME/Applications" -maxdepth 2 -name TradingView.app -type d 2>/dev/null | head -n 1 || true)"
  if [[ -n "$candidate" && -x "$candidate/Contents/MacOS/TradingView" ]]; then
    bundle="$candidate"
  fi
fi

if [[ -z "$bundle" ]]; then
  echo "TradingView.app was not found in /Applications or ~/Applications." >&2
  echo "Install TradingView Desktop, open it once, sign in, and retry." >&2
  exit 69
fi

binary="$bundle/Contents/MacOS/TradingView"

echo "Closing any normally launched TradingView instance..."
osascript -e 'tell application "TradingView" to quit' >/dev/null 2>&1 || true
for _ in {1..30}; do
  if ! pgrep -f '/TradingView.app/Contents/MacOS/TradingView' >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done
if pgrep -f '/TradingView.app/Contents/MacOS/TradingView' >/dev/null 2>&1; then
  pkill -TERM -f '/TradingView.app/Contents/MacOS/TradingView' 2>/dev/null || true
  sleep 1
fi

: >"$log_file"
echo "Launching $bundle with local CDP on port $port..."
nohup "$binary" \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port="$port" \
  >>"$log_file" 2>&1 &
launcher_pid=$!

for _ in {1..60}; do
  if cdp_ready && chart_ready; then
    echo "TradingView is ready at $endpoint"
    echo "Log: $log_file"
    echo "Use this launcher again after a full TradingView restart or Mac reboot."
    exit 0
  fi
  if ! kill -0 "$launcher_pid" 2>/dev/null && ! pgrep -f '/TradingView.app/Contents/MacOS/TradingView' >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

echo "TradingView started, but no chart target appeared at $endpoint." >&2
echo "Open a TradingView chart and check: curl $endpoint/json/list" >&2
echo "Log: $log_file" >&2
exit 1
