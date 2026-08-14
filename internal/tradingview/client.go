package tradingview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultCDPURL  = "http://127.0.0.1:9222"
	defaultTimeout = 3 * time.Second
	maxCDPBody     = 1 << 20
)

var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.\-]{0,14}$`)

// Status describes whether the optional local TradingView integration is
// configured and whether a chart target is currently reachable.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

type cdpConnection interface {
	WriteJSON(any) error
	ReadJSON(any) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	SetReadLimit(int64)
	Close() error
}

type dialFunc func(context.Context, string, http.Header) (cdpConnection, *http.Response, error)

// Client controls the active chart in a locally running TradingView Desktop
// instance through Chrome DevTools Protocol. It is safe for concurrent use.
type Client struct {
	enabled   bool
	cdpURL    *url.URL
	timeout   time.Duration
	http      *http.Client
	dial      dialFunc
	configErr error
	mu        sync.Mutex
}

// NewFromEnv constructs the opt-in integration. godotenv is loaded by the
// application before this function runs, so values may come from the repo's
// .env file or the process environment.
func NewFromEnv() *Client {
	enabled := false
	var configErr error
	if raw := strings.TrimSpace(os.Getenv("WATCHLIST_TRADINGVIEW_ENABLED")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			configErr = errors.Join(configErr, fmt.Errorf("WATCHLIST_TRADINGVIEW_ENABLED: %w", err))
		} else {
			enabled = value
		}
	}

	base := strings.TrimSpace(os.Getenv("WATCHLIST_TRADINGVIEW_CDP_URL"))
	if base == "" {
		base = defaultCDPURL
	}
	parsed, err := parseLoopbackHTTPURL(base)
	if err != nil {
		configErr = errors.Join(configErr, fmt.Errorf("WATCHLIST_TRADINGVIEW_CDP_URL: %w", err))
		parsed, _ = parseLoopbackHTTPURL(defaultCDPURL)
	}

	timeout := defaultTimeout
	if raw := strings.TrimSpace(os.Getenv("WATCHLIST_TRADINGVIEW_TIMEOUT")); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			configErr = errors.Join(configErr, fmt.Errorf("WATCHLIST_TRADINGVIEW_TIMEOUT must be a positive duration"))
		} else {
			timeout = value
		}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &websocket.Dialer{HandshakeTimeout: timeout, Proxy: nil}
	return &Client{
		enabled:   enabled,
		cdpURL:    parsed,
		timeout:   timeout,
		http:      &http.Client{Timeout: timeout, Transport: transport},
		configErr: configErr,
		dial: func(ctx context.Context, endpoint string, header http.Header) (cdpConnection, *http.Response, error) {
			return dialer.DialContext(ctx, endpoint, header)
		},
	}
}

func parseLoopbackHTTPURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("must use http or https")
	}
	if parsed.Host == "" || !loopbackHost(parsed.Hostname()) {
		return nil, errors.New("must use a loopback host such as 127.0.0.1")
	}
	return parsed, nil
}

func loopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validLoopbackWebSocketURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
		return false
	}
	return loopbackHost(parsed.Hostname())
}

func (c *Client) Enabled() bool { return c != nil && c.enabled }

// Status performs a lightweight lookup of TradingView's local CDP targets.
func (c *Client) Status(ctx context.Context) Status {
	status := Status{Enabled: c.Enabled()}
	if !status.Enabled {
		return status
	}
	if c.configErr != nil {
		status.Error = c.configErr.Error()
		return status
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	_, err := c.findChartTarget(ctx)
	status.Connected = err == nil
	if err != nil {
		status.Error = err.Error()
	}
	return status
}

// SetSymbol changes the active TradingView chart symbol.
func (c *Client) SetSymbol(ctx context.Context, symbol string) error {
	if !c.Enabled() {
		return errors.New("TradingView Desktop integration is disabled")
	}
	if c.configErr != nil {
		return c.configErr
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if !symbolPattern.MatchString(symbol) {
		return errors.New("invalid symbol")
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	// Multiple Watchlist Tool browser tabs can click at once. Keep CDP symbol
	// changes ordered so an earlier request cannot finish after a later one.
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setSymbol(ctx, symbol)
}

func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *Client) setSymbol(ctx context.Context, symbol string) error {
	target, err := c.findChartTarget(ctx)
	if err != nil {
		return err
	}
	if !validLoopbackWebSocketURL(target.WebSocketDebuggerURL) {
		return errors.New("TradingView returned a non-loopback CDP WebSocket URL")
	}

	connection, handshakeResponse, err := c.dial(ctx, target.WebSocketDebuggerURL, nil)
	if handshakeResponse != nil && handshakeResponse.Body != nil {
		defer handshakeResponse.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect to TradingView CDP target: %w", err)
	}
	defer connection.Close()
	connection.SetReadLimit(maxCDPBody)

	deadline := time.Now().Add(c.timeout)
	if value, ok := ctx.Deadline(); ok {
		deadline = value
	}
	if err := connection.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set TradingView CDP write deadline: %w", err)
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("set TradingView CDP read deadline: %w", err)
	}

	request := struct {
		ID     int            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}{
		ID:     1,
		Method: "Runtime.evaluate",
		Params: map[string]any{
			"expression":    setSymbolExpression(symbol),
			"awaitPromise":  true,
			"returnByValue": true,
		},
	}
	if err := connection.WriteJSON(request); err != nil {
		return fmt.Errorf("send TradingView CDP command: %w", err)
	}

	for {
		var message struct {
			ID    int `json:"id"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
			Result *struct {
				Result *struct {
					Value struct {
						Current string `json:"current"`
					} `json:"value"`
				} `json:"result,omitempty"`
				ExceptionDetails *struct {
					Text      string `json:"text"`
					Exception *struct {
						Description string `json:"description"`
					} `json:"exception,omitempty"`
				} `json:"exceptionDetails,omitempty"`
			} `json:"result,omitempty"`
		}
		if err := connection.ReadJSON(&message); err != nil {
			return fmt.Errorf("read TradingView CDP response: %w", err)
		}
		// CDP events have no matching request ID. Ignore them until the
		// Runtime.evaluate response arrives.
		if message.ID != request.ID {
			continue
		}
		if message.Error != nil {
			return fmt.Errorf("TradingView CDP error %d: %s", message.Error.Code, message.Error.Message)
		}
		if message.Result != nil && message.Result.ExceptionDetails != nil {
			text := strings.TrimSpace(message.Result.ExceptionDetails.Text)
			if exception := message.Result.ExceptionDetails.Exception; exception != nil && strings.TrimSpace(exception.Description) != "" {
				text = strings.TrimSpace(exception.Description)
			}
			if text == "" {
				text = "TradingView rejected the symbol change"
			}
			return errors.New(text)
		}
		if message.Result != nil && message.Result.Result != nil {
			current := message.Result.Result.Value.Current
			if current != "" && !symbolMatches(current, symbol) {
				return fmt.Errorf("TradingView reported %q after requesting %q", current, symbol)
			}
		}
		return nil
	}
}

func symbolMatches(current, requested string) bool {
	current = strings.ToUpper(strings.TrimSpace(current))
	requested = strings.ToUpper(strings.TrimSpace(requested))
	return current == requested || strings.HasSuffix(current, ":"+requested)
}

func (c *Client) findChartTarget(ctx context.Context) (cdpTarget, error) {
	endpoint := *c.cdpURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/json/list"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return cdpTarget{}, fmt.Errorf("prepare TradingView target request: %w", err)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return cdpTarget{}, fmt.Errorf("query TradingView debug port: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = response.Status
		}
		return cdpTarget{}, fmt.Errorf("TradingView debug port returned %s", message)
	}

	var targets []cdpTarget
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxCDPBody))
	if err := decoder.Decode(&targets); err != nil {
		return cdpTarget{}, fmt.Errorf("decode TradingView targets: %w", err)
	}
	if target, ok := selectChartTarget(targets); ok {
		return target, nil
	}
	return cdpTarget{}, errors.New("no TradingView chart target found")
}

func selectChartTarget(targets []cdpTarget) (cdpTarget, bool) {
	var fallback cdpTarget
	for _, target := range targets {
		if target.Type != "page" || !validLoopbackWebSocketURL(target.WebSocketDebuggerURL) {
			continue
		}
		urlText := strings.ToLower(target.URL)
		if strings.Contains(urlText, "tradingview.com/chart") {
			return target, true
		}
		text := strings.ToLower(target.URL + " " + target.Title)
		if fallback.WebSocketDebuggerURL == "" && strings.Contains(text, "tradingview") {
			fallback = target
		}
	}
	return fallback, fallback.WebSocketDebuggerURL != ""
}

func setSymbolExpression(symbol string) string {
	quoted, _ := json.Marshal(symbol)
	literal := string(quoted)
	return `(function() {
  var api = window.TradingViewApi;
  if (!api || !api._activeChartWidgetWV || typeof api._activeChartWidgetWV.value !== 'function') {
    throw new Error('TradingView chart API is unavailable');
  }
  var chart = api._activeChartWidgetWV.value();
  if (!chart || typeof chart.setSymbol !== 'function') {
    throw new Error('TradingView active chart is unavailable');
  }
  return new Promise(function(resolve) {
    chart.setSymbol(` + literal + `, {});
    setTimeout(function() {
      var current = '';
      try {
        if (typeof chart.symbol === 'function') current = chart.symbol();
      } catch (_) {}
      resolve({ requested: ` + literal + `, current: current });
    }, 500);
  });
})()`
}
