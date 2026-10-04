package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"
	"watchlist-tool/internal/config"
	"watchlist-tool/internal/market"
	"watchlist-tool/internal/model"
	"watchlist-tool/internal/server"
)

func main() {
	mode := flag.String("mode", "live", "live or demo")
	path := flag.String("config", "config.yaml", "config path")
	addr := flag.String("addr", "", "listen override")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	if *addr != "" {
		cfg.App.Addr = *addr
	}
	store, err := model.Open(cfg.App.DataFile)
	if err != nil {
		log.Fatal(err)
	}
	hub := market.NewHub(cfg.Display.HistoryPoints)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var feed interface {
		Run(context.Context)
		SetSymbols([]string)
	}
	if *mode == "demo" {
		feed = market.NewDemo(hub)
	} else if *mode == "live" {
		if cfg.Provider == "massive" {
			feed = market.NewGateway(cfg, hub)
		} else {
			feed = market.NewIBKR(cfg, hub)
		}
	} else {
		log.Fatalf("unknown mode %q", *mode)
	}
	srv := server.New(cfg, store, hub, feed)
	srv.InitialSync()
	go feed.Run(ctx)
	log.Printf("watchlist-tool mode=%s provider=%s listening=%s", *mode, cfg.Provider, cfg.App.Addr)
	if err = srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
	log.Print("shutdown complete")
}
