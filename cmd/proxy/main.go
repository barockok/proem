package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/barockok/pro-ant/internal/app"
	"github.com/barockok/pro-ant/internal/client"
	"github.com/barockok/pro-ant/internal/config"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "issue-token" {
		if len(os.Args) != 3 || os.Args[2] == "" {
			log.Fatal(fmt.Errorf("usage: pro-ant issue-token <client-name>"))
		}
		if err := client.IssueAndDescribe(os.Args[2], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

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
