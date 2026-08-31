package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/barockok/pro-ant/internal/app"
	"github.com/barockok/pro-ant/internal/config"
)

func main() {
	cfg, err := config.ParseFlags()
	if err != nil {
		log.Fatal(err)
	}

	a, err := app.New(cfg)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer a.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
