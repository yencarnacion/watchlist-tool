# watchlist-tool

A fast, dark-mode IBKR watchlist board for scalping. It displays live last price,
percent change, cumulative day volume, and a compact day map; supports multiple
named watchlists, drag/drop and arrow reordering; and coordinates one-click ticker
selection with `tape-reading-tool` and `polygon-charts`.

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
- `POST /api/select` — switch tape-reading-tool and return the chart deep link
- `GET /api/events` — live Server-Sent Events stream

Clicking a ticker opens today's polygon-charts URL in a new tab and posts the
same ticker to `http://127.0.0.1:8097/api/ticker`. Press `/` anywhere to focus the
add box.
