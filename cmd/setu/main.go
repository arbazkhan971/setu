// Command setu is an OpenAI-compatible LLM gateway: one API in front of
// every provider, with routing, fallbacks, and load balancing.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/arbazkhan971/setu/config"
	"github.com/arbazkhan971/setu/metrics"
	"github.com/arbazkhan971/setu/server"

	// Register built-in providers.
	_ "github.com/arbazkhan971/setu/providers/anthropic"
	_ "github.com/arbazkhan971/setu/providers/cohere"
	_ "github.com/arbazkhan971/setu/providers/compat"
	_ "github.com/arbazkhan971/setu/providers/gemini"
	_ "github.com/arbazkhan971/setu/providers/mock"
	_ "github.com/arbazkhan971/setu/providers/openai"
)

// version is overridden at build time via -ldflags.
var version = "0.1.0"

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	port := flag.Int("port", 0, "listen port (overrides config; default 4000)")
	flag.Parse()

	if flag.Arg(0) == "version" {
		fmt.Println("setu", version)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	gw, err := cfg.BuildGateway()
	if err != nil {
		fatal("build gateway: %v", err)
	}

	listenPort := cfg.Server.Port
	if *port != 0 {
		listenPort = *port
	}
	if listenPort == 0 {
		listenPort = 4000
	}

	srv := server.New(gw, cfg.MasterKey()).
		WithPolicy(cfg.BuildEnforcer()).
		WithCache(cfg.BuildCache())
	if cfg.Server.Metrics {
		srv = srv.WithMetrics(metrics.New())
	}
	addr := fmt.Sprintf(":%d", listenPort)
	slog.Info("setu listening", "addr", addr, "version", version, "models", gw.Models())
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		fatal("server: %v", err)
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "setu: "+format+"\n", a...)
	os.Exit(1)
}
