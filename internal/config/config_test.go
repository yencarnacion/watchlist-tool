package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicDefaultsAndLocalGateway(t *testing.T) {
	t.Setenv("MARKET_DATA_PROVIDER", "")
	t.Setenv("MARKET_DATA_GATEWAY_URL", "")
	t.Setenv("MARKET_DATA_GATEWAY_TOKEN", "")
	c, e := Load("../../config.yaml")
	if e != nil || c.Provider != "ibkr" || c.Gateway.APIURL != "" {
		t.Fatal(c.Provider, e)
	}
	t.Setenv("MARKET_DATA_PROVIDER", "massive")
	t.Setenv("MARKET_DATA_GATEWAY_URL", "http://127.0.0.1:1234/adapter")
	t.Setenv("MARKET_DATA_GATEWAY_TOKEN", "private-test-token")
	c, e = Load("../../config.yaml")
	if e != nil || c.Provider != "massive" {
		t.Fatal(e)
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "private-test-token") || strings.Contains(string(b), "1234") {
		t.Fatal("private config leaked")
	}
}
