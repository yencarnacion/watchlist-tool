package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	App struct {
		Addr     string `yaml:"addr"`
		Timezone string `yaml:"timezone"`
		DataFile string `yaml:"data_file"`
	} `yaml:"app"`
	IBKR struct {
		Host              string `yaml:"host"`
		Port              int    `yaml:"port"`
		ClientID          int64  `yaml:"client_id"`
		Exchange          string `yaml:"exchange"`
		Currency          string `yaml:"currency"`
		MarketDataType    int64  `yaml:"market_data_type"`
		ReconnectInterval string `yaml:"reconnect_interval"`
	} `yaml:"ibkr"`
	Integrations struct {
		TapeURL    string `yaml:"tape_url"`
		PolygonURL string `yaml:"polygon_url"`
	} `yaml:"integrations"`
	Display struct {
		HistoryPoints int    `yaml:"history_points"`
		StaleAfter    string `yaml:"stale_after"`
	} `yaml:"display"`
}

func Load(path string) (Config, error) {
	_ = godotenv.Load()
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err = yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if v := os.Getenv("PORT"); v != "" {
		c.App.Addr = ":" + v
	}
	if v := os.Getenv("IBKR_HOST"); v != "" {
		c.IBKR.Host = v
	}
	if v := os.Getenv("IBKR_PORT"); v != "" {
		c.IBKR.Port, err = strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("IBKR_PORT: %w", err)
		}
	}
	if v := os.Getenv("IBKR_CLIENT_ID"); v != "" {
		c.IBKR.ClientID, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("IBKR_CLIENT_ID: %w", err)
		}
	}
	if c.IBKR.ClientID == 97 {
		return c, fmt.Errorf("ibkr client_id 97 conflicts with tape-reading-tool; use a unique ID such as 98")
	}
	if c.App.Addr == "" || c.IBKR.Host == "" || c.IBKR.Port < 1 || c.IBKR.Exchange == "" {
		return c, fmt.Errorf("invalid app/IBKR configuration")
	}
	if _, err = time.ParseDuration(c.IBKR.ReconnectInterval); err != nil {
		return c, fmt.Errorf("reconnect_interval: %w", err)
	}
	if c.Display.HistoryPoints < 60 {
		c.Display.HistoryPoints = 720
	}
	return c, nil
}
