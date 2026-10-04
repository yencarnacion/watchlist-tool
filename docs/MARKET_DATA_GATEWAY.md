# Optional market-data gateway v1

Public defaults select IBKR. Local environment selects `MARKET_DATA_PROVIDER=massive`,
`MARKET_DATA_GATEWAY_URL` (the adapter's base path), and optional
`MARKET_DATA_GATEWAY_TOKEN`. Tokens stay in Authorization headers on backend
requests. Use loopback HTTP or remote HTTPS; redirects and foreign pagination
are rejected. No URL or token is sent to browsers. This is a public protocol,
not a dependency on any particular private application.

The adapter serves Massive-compatible JSON under `/rest` and SSE under `/stream`.
It owns the provider credential, shared upstream socket, rate limits and cache.
Clients never add a provider socket when gateway mode is selected.

GET `/stream?symbols=TEST&channels=Q,A,AM,T` returns `hello` with
`api_version:1`, `source:massive` and `feed:realtime` or `delayed`.
`market` events contain `channel`, `symbol`, `event_ms`, `received_ms` and
original provider JSON in `data`. Send `heartbeat` every five seconds and
`gap` on disconnect/loss. Clients resynchronize on every connection, gap or
symbol switch; a 20-second silent stream is disconnected. The gateway feed is
best effort, not a lossless/exactly-once recorder. Timestamps are source event
times; heartbeat and retrieval times never establish price freshness.

REST routes used:
- `/rest/v2/snapshot/locale/us/markets/stocks/tickers/{symbol}`
- `/rest/v2/aggs/ticker/{symbol}/range/1/minute/{from}/{to}`
- `/rest/v3/reference/tickers/{symbol}` (watchlist names)
- `/rest/v3/trades/{symbol}` and `/rest/v3/quotes/{symbol}` (tape preparation)

`next_url` must be a relative path within the configured adapter base and
`/rest/`. Return complete pages and upstream error statuses. Integer timestamps
must retain precision. Aggregates use milliseconds; REST trades/quotes use
nanoseconds. Unadjusted bars maintain consistency with raw prices. Share sizes
are not multiplied by 100. See [Massive's quote size specification](https://massive.com/blog/change-stocks-quotes-round-lots-to-shares).

Keep real addresses, secrets, account data, session files and screenshots in
ignored local files. A missing/unavailable adapter must not change public IBKR
behavior or silently switch a configured Massive client back to IBKR.
