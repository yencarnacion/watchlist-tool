# TradingView Desktop ticker integration on macOS

Watchlist Tool can send one ticker click to two independent destinations:

1. Tape Reading Tool
2. The active chart in TradingView Desktop

The existing chart button continues to open Polygon Charts separately. If
TradingView is unavailable, Tape Reading Tool still receives the ticker. If Tape
Reading Tool is unavailable, Watchlist Tool still attempts the TradingView
change.

## How it works

TradingView Desktop is an Electron application. When it is launched with a
loopback Chrome DevTools Protocol (CDP) port, Watchlist Tool can locate its chart
page and evaluate the active chart's internal `setSymbol()` method.

The implementation is native Go and uses Watchlist Tool's own short-lived CDP
WebSocket connection. It does not require Node.js, npm, Claude Code, an MCP
client, or a separately running `tradingview-mcp` process.

The technique and internal TradingView chart path were informed by the MIT
licensed project at `https://github.com/tradesdontlie/tradingview-mcp`.
TradingView's internal chart API is undocumented and can change in a future
TradingView Desktop release.

## One-time setup

### 1. Install and sign in to TradingView Desktop

Install TradingView in one of the normal macOS locations:

- `/Applications/TradingView.app`
- `~/Applications/TradingView.app`

Open it normally once and sign in to your TradingView account.

### 2. Enable the integration in Watchlist Tool

Add these values to the `.env` file in the Watchlist Tool repository:

```dotenv
WATCHLIST_TRADINGVIEW_ENABLED=true
WATCHLIST_TRADINGVIEW_CDP_URL=http://127.0.0.1:9222
WATCHLIST_TRADINGVIEW_TIMEOUT=3s
```

The feature is deliberately disabled by default in `.env.example` because this
is a public repository and not every installation runs TradingView Desktop.
None of these values is a secret.

Restart Watchlist Tool after changing `.env`.

### 3. Make the included launcher executable

```bash
chmod +x scripts/launch-tradingview-debug-mac.sh
```

Git normally preserves this executable bit.

## Start TradingView with the local debug port

A normal Dock or Finder launch does not expose the CDP endpoint. From the
Watchlist Tool repository, run:

```bash
./scripts/launch-tradingview-debug-mac.sh
```

The script:

- finds `TradingView.app`
- closes an existing normally launched TradingView process
- relaunches it with CDP bound only to `127.0.0.1:9222`
- waits for a TradingView chart target
- writes the process log to `~/.watchlist-tool/tradingview-debug.log`

Save unsaved Pine Editor work before the first relaunch.

A different port is supported:

```bash
./scripts/launch-tradingview-debug-mac.sh 9333
```

Update `.env` to match:

```dotenv
WATCHLIST_TRADINGVIEW_CDP_URL=http://127.0.0.1:9333
```

## Sharing TradingView with DaiDai and other local tools

Only one debug-enabled TradingView Desktop instance is needed. Watchlist Tool
and DaiDai can both connect to the same `127.0.0.1:9222` endpoint. Either
repository's launcher may start it; the included launchers detect an already
ready instance and exit without starting another one.

This integration creates no new Watchlist Tool listening port. It uses the
existing Watchlist Tool port (`8098` by default) and makes an outbound loopback
connection to TradingView on `9222`. It therefore does not conflict with:

- Polygon Charts on `8081`
- Tape Reading Tool on `8097`
- DaiDai on its configured port
- Yamir Trading Tools on its configured ports
- IB Gateway or TWS

Keep unique IBKR client IDs for applications that connect to IBKR. The
TradingView integration itself does not use IBKR.

When different applications send symbols close together, TradingView displays
the most recently completed request. In normal use, the last ticker clicked is
the ticker shown.

## Verify the TradingView endpoint

```bash
curl --noproxy '*' -s http://127.0.0.1:9222/json/version | python3 -m json.tool
```

```bash
curl --noproxy '*' -s http://127.0.0.1:9222/json/list | python3 -m json.tool
```

The target list should include a page whose URL or title identifies TradingView,
normally a URL containing `tradingview.com/chart`.

## Start Watchlist Tool

```bash
./go.sh live
```

Open `http://127.0.0.1:8098` on the same Mac. Clicking the ticker portion of an
existing row now calls `/api/select`, which sends the normalized ticker to Tape
Reading Tool and TradingView concurrently.

The row's chart-arrow button remains a separate action and continues to open the
Polygon Charts URL. The header reports `TV OFF`, `TV READY`, `TV TICKER`,
`TV OFFLINE`, or `TV ERROR`; a failed symbol command also produces a toast.

## Status and direct tests

Check whether Watchlist Tool can see TradingView:

```bash
curl -s http://127.0.0.1:8098/api/integrations/tradingview/status | python3 -m json.tool
```

Expected when ready:

```json
{
  "enabled": true,
  "connected": true
}
```

Change TradingView directly to AAPL through Watchlist Tool:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d '{"symbol":"AAPL"}' \
  http://127.0.0.1:8098/api/integrations/tradingview/ticker
```

Test the same path used by a ticker-row click:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -d '{"symbol":"NVDA"}' \
  http://127.0.0.1:8098/api/select | python3 -m json.tool
```

A successful response includes independent results:

```json
{
  "symbol": "NVDA",
  "tape_ok": true,
  "tradingview_enabled": true,
  "tradingview_ok": true
}
```

## Daily startup sequence

Start the shared TradingView instance once, then start the local tools in any
order:

```bash
cd /path/to/watchlist-tool
./scripts/launch-tradingview-debug-mac.sh
./go.sh live
```

After a full TradingView quit, crash, update, or Mac restart, use the launcher
again. Reopening TradingView from the Dock does not include the required debug
flags.

## Troubleshooting

### Status says `enabled: false`

Confirm `.env` contains:

```dotenv
WATCHLIST_TRADINGVIEW_ENABLED=true
```

Restart Watchlist Tool so `godotenv` reloads the file.

### Status says `connected: false`

Run:

```bash
curl --noproxy '*' -i http://127.0.0.1:9222/json/version
curl --noproxy '*' -i http://127.0.0.1:9222/json/list
```

If either request fails, quit TradingView completely and run:

```bash
./scripts/launch-tradingview-debug-mac.sh
```

If `/json/version` works but `/json/list` contains no TradingView chart page,
open a chart in TradingView and retry.

### Port 9222 is already occupied

```bash
lsof -nP -iTCP:9222 -sTCP:LISTEN
```

Use another port only when the existing listener is not the intended
TradingView instance, and update every local tool's CDP URL to the same new port.

### The wrong TradingView pane or window changes

The integration selects the first CDP page whose URL identifies a TradingView
chart and changes that page's active chart widget. For predictable behavior,
keep the desired TradingView pane active and use one TradingView Desktop window
while validating the setup.

### It stopped working after a TradingView update

Confirm the CDP endpoints still respond and inspect:

```bash
tail -100 ~/.watchlist-tool/tradingview-debug.log
```

If CDP works but symbol changes fail, TradingView may have changed its
undocumented internal chart API. Compare the current behavior with the upstream
`tradingview-mcp` project.

## Security

CDP can control the running application. The implementation therefore:

- accepts only loopback CDP HTTP and WebSocket addresses
- disables proxy use for CDP traffic
- binds the launcher to `127.0.0.1`
- refuses TradingView control requests received from non-loopback clients

Do not expose or forward the CDP port through a router, tunnel, public interface,
or remote port-forward.
