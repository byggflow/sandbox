package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/daemon"
)

func main() {
	configPath := flag.String("config", "", "path to config file (TOML)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Load configuration.
	var cfg config.Config
	if *configPath != "" {
		var err error
		cfg, err = config.Load(*configPath)
		if err != nil {
			log.Error("failed to load config", "path", *configPath, "error", err)
			os.Exit(1)
		}
		log.Info("loaded config", "path", *configPath)
	} else {
		cfg = config.Defaults()
		log.Info("using default config (no --config specified)")
	}

	// Create daemon.
	d, err := daemon.New(cfg, log)
	if err != nil {
		log.Error("failed to create daemon", "error", err)
		os.Exit(1)
	}

	// Start daemon.
	ctx := context.Background()
	if err := d.Start(ctx); err != nil {
		log.Error("failed to start daemon", "error", err)
		os.Exit(1)
	}

	// Handle signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)

	for {
		sig := <-sigCh
		switch sig {
		case syscall.SIGHUP:
			if *configPath != "" {
				log.Info("received SIGHUP, reloading config")
				if err := d.Reload(*configPath); err != nil {
					log.Error("config reload failed", "error", err)
				}
			} else {
				log.Warn("received SIGHUP but no config file specified, ignoring")
			}
		case syscall.SIGTERM, syscall.SIGINT:
			log.Info("received shutdown signal", "signal", sig.String())
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30_000_000_000) // 30s
			defer cancel()
			if err := d.Shutdown(shutdownCtx); err != nil {
				log.Error("shutdown error", "error", err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "sandboxd: stopped")
			os.Exit(0)
		}
	}
}
