// Package marketgateway consumes a configured, server-side market-data adapter.
// Credentials and deployment addresses are supplied locally, never to browsers.
package marketgateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type Client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func Validate(raw string) error {
	u, e := url.Parse(strings.TrimRight(raw, "/"))
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.Contains(u.Path, "..") {
		return errors.New("market-data gateway requires an HTTP(S) URL without credentials, query or fragment")
	}
	h := strings.ToLower(u.Hostname())
	ip := net.ParseIP(h)
	if h == "massive.com" || strings.HasSuffix(h, ".massive.com") || h == "polygon.io" || strings.HasSuffix(h, ".polygon.io") {
		return errors.New("configure a market-data gateway, not a direct provider URL")
	}
	if u.Scheme == "http" && h != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("non-loopback market-data gateways require HTTPS")
	}
	return nil
}
func New(raw, token string) (*Client, error) {
	if e := Validate(raw); e != nil {
		return nil, e
	}
	u, _ := url.Parse(strings.TrimRight(raw, "/"))
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 10 * time.Second
	return &Client{base: u, token: token, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) Request(ctx context.Context, path string) (*http.Response, error) {
	u, e := url.Parse(path)
	if e != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || strings.Contains(u.Path, "..") || strings.Contains(u.Path, "\\") || !strings.HasPrefix(u.Path, "/") {
		return nil, errors.New("unsafe gateway path")
	}
	q := u.Query()
	for k := range q {
		if strings.EqualFold(k, "apiKey") || strings.EqualFold(k, "token") {
			return nil, errors.New("credentials in gateway query are forbidden")
		}
	}
	if strings.HasPrefix(u.Path, "/rest/") || u.Path == "/stream" || u.Path == "/health" {
		u.Path = c.base.Path + u.Path
	} else if !strings.HasPrefix(u.Path, c.base.Path+"/rest/") || c.base.Path == "" {
		return nil, errors.New("pagination must stay inside the configured gateway")
	}
	u.Scheme = c.base.Scheme
	u.Host = c.base.Host
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, errors.New("invalid gateway request")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	res, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("market-data gateway unavailable or timed out")
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("market-data gateway HTTP %d", res.StatusCode)
	}
	return res, nil
}
func (c *Client) Get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, e := c.Request(ctx, path)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		return errors.New("gateway returned non-JSON data")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if e != nil || len(b) > 16<<20 {
		return errors.New("gateway response incomplete or oversized")
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("invalid gateway JSON")
	}
	return nil
}

type Event struct {
	Channel, Symbol string
	EventMS         int64 `json:"event_ms"`
	ReceivedMS      int64 `json:"received_ms"`
	Data            json.RawMessage
}
type Hello struct {
	APIVersion   int `json:"api_version"`
	Source, Feed string
}

// Stream reconnects only under caller control: every failure requires REST resync.
// Heartbeats prove transport health, never freshness of a stock's quote.
func (c *Client) Stream(ctx context.Context, symbols, channels string, fn func(string, json.RawMessage) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res, e := c.Request(ctx, "/stream?"+url.Values{"symbols": {symbols}, "channels": {channels}}.Encode())
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		return errors.New("gateway stream unavailable")
	}
	var last atomic.Int64
	last.Store(time.Now().UnixMilli())
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Now().UnixMilli()-last.Load() > 20000 {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	event := ""
	var data []byte
	hello := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(line[6:])
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, []byte(strings.TrimSpace(line[5:]))...)
			if len(data) > 1<<20 {
				return errors.New("oversized gateway event")
			}
			data = append(data, '\n')
		} else if line == "" {
			if event == "" {
				continue
			}
			last.Store(time.Now().UnixMilli())
			switch event {
			case "hello":
				var h Hello
				if hello || json.Unmarshal(data, &h) != nil || h.Source != "massive" || h.APIVersion != 1 || (h.Feed != "realtime" && h.Feed != "delayed") {
					return errors.New("unsupported gateway handshake")
				}
				hello = true
			case "gap":
				return errors.New("gateway stream gap; resync required")
			case "market", "heartbeat":
				if !hello {
					return errors.New("gateway handshake missing")
				}
			default:
				event = ""
				data = nil
				continue
			}
			if e := fn(event, json.RawMessage(data)); e != nil {
				return e
			}
			event = ""
			data = nil
		}
	}
	return errors.New("gateway stream disconnected; resync required")
}
func Millis(t int64) int64 {
	if t >= 100000000000000000 {
		return t / 1000000
	}
	if t >= 100000000000000 {
		return t / 1000
	}
	return t
}

type Bar struct {
	T  int64   `json:"t"`
	O  float64 `json:"o"`
	H  float64 `json:"h"`
	L  float64 `json:"l"`
	C  float64 `json:"c"`
	V  float64 `json:"v"`
	VW float64 `json:"vw"`
}

func (c *Client) Bars(ctx context.Context, symbol, span string, start, end time.Time) ([]Bar, error) {
	path := fmt.Sprintf("/rest/v2/aggs/ticker/%s/range/1/%s/%d/%d?adjusted=false&sort=asc&limit=50000", url.PathEscape(symbol), span, start.UnixMilli(), end.UnixMilli()-1)
	out := []Bar{}
	seen := map[string]bool{}
	for page := 0; path != ""; page++ {
		if page >= 30 || seen[path] {
			return nil, errors.New("gateway history pagination limit")
		}
		seen[path] = true
		var v struct {
			Results []Bar  `json:"results"`
			Next    string `json:"next_url"`
		}
		if e := c.Get(ctx, path, &v); e != nil {
			return nil, e
		}
		for _, b := range v.Results {
			if b.T >= start.UnixMilli() && b.T < end.UnixMilli() && b.C > 0 {
				out = append(out, b)
			}
		}
		path = v.Next
	}
	return out, nil
}
