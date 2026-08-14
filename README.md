# watchlist-tool

A fast, dark-mode IBKR watchlist board for scalping. It displays live last price,
percent change, cumulative volume since 04:00 ET, and a compact day map; supports multiple
named watchlists, drag/drop and arrow reordering; and coordinates one-click ticker
selection with `tape-reading-tool`, TradingView Desktop, and `polygon-charts`.

## Run

Requirements: Go 1.23+, TWS or IB Gateway with its socket API enabled, and live
market-data permissions.

```bash
./go.sh demo   # synthetic quotes, no IBKR required
./go.sh live   # connect to IBKR
```

Open <http://localhost:8098>. `Ctrl-C` cleanly stops the HTTP and IBKR clients.
Copy `.env.example` when setting up another machine. The included `.env` uses
IBKR client ID `98`, deliberately different from tape-reading-tool's default `97`,
so both can share TWS/Gateway. The app also uses its own web port (`8098`).

Configuration lives in `config.yaml`. Saved lists live in
`data/watchlists.json`. The file is replaced atomically after every change.

## Optional TradingView Desktop integration

A ticker-row click can also change the active chart in the locally running
TradingView Desktop app. The integration is opt-in and uses a loopback-only
Chrome DevTools Protocol endpoint; it does not require Node.js or a separately
running MCP server.

Enable it in `.env`:

```dotenv
WATCHLIST_TRADINGVIEW_ENABLED=true
WATCHLIST_TRADINGVIEW_CDP_URL=http://127.0.0.1:9222
WATCHLIST_TRADINGVIEW_TIMEOUT=3s
```

Launch TradingView with the included macOS helper before starting Watchlist Tool:

```bash
./scripts/launch-tradingview-debug-mac.sh
```

One debug-enabled TradingView instance can be shared with DaiDai and other local
tools. The header shows `TV OFF`, `TV READY`, the most recently selected ticker,
or `TV ERROR`. Full setup, verification, multi-app operation, security, and
troubleshooting are documented in
[`docs/TRADINGVIEW_DESKTOP_INTEGRATION.md`](docs/TRADINGVIEW_DESKTOP_INTEGRATION.md).

## Scanner / daidai API

Add a ticker to the first list:

```bash
curl -X POST http://localhost:8098/api/tickers \
  -H 'Content-Type: application/json' \
  -d '{"symbol":"NVDA","note":"daidai momentum"}'
```

Choose a list by its `ID` from `GET /api/state`:

```json
{"ticker":"NVDA","listID":"focus","note":"repeat alert"}
```

`ticker` and `symbol` are accepted aliases. Repeated posts never create a
duplicate: the existing ticker is moved to the top, optionally moved to the
requested list, and its non-empty note is refreshed. Every open browser updates
immediately.

## API

- `GET /api/state` — layout, quote snapshot, status, integration config
- `POST /api/tickers` — add or promote a symbol
- `DELETE /api/tickers/{id}` — remove a ticker
- `PUT /api/layout` — persist list names and full ordering
- `POST /api/select` — independently switch Tape Reading Tool and TradingView Desktop
- `POST /api/chart` — return today's Polygon Charts deep link
- `GET /api/integrations/tradingview/status` — report whether local TradingView CDP is ready
- `POST /api/integrations/tradingview/ticker` — change the active TradingView chart directly
- `GET /api/events` — live Server-Sent Events stream

Clicking the ticker portion of a row posts the same normalized ticker to
`http://127.0.0.1:8097/api/ticker` and, when enabled, to the active TradingView
Desktop chart. The two requests run independently, so either application can be
offline without blocking the other. The chart-arrow button separately opens
today's Polygon Charts URL. Press `/` anywhere to focus the add box.
