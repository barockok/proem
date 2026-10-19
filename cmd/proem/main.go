package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/barockok/proem/internal/app"
	"github.com/barockok/proem/internal/client"
	"github.com/barockok/proem/internal/config"
)

// version is stamped at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-version") {
		fmt.Println("proem", version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "issue-token" {
		if len(os.Args) != 3 || os.Args[2] == "" {
			log.Fatal(fmt.Errorf("usage: proem issue-token <client-name>"))
		}
		if err := client.IssueAndDescribe(os.Args[2], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}

	// The container image has no shell or curl, so container healthchecks
	// call the binary itself. TLS trust follows SSL_CERT_FILE.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if len(os.Args) != 3 {
			log.Fatal(fmt.Errorf("usage: proem healthcheck <url>"))
		}
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("healthcheck: status %d", resp.StatusCode)
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
