package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/daemon"
	"github.com/fsnotify/fsnotify"
)

var version = "0.0.0"

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

	// Apply environment variable overrides (take precedence over config file).
	cfg.ApplyEnvOverrides()

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

	// Watch config file for changes (hot-reload).
	if *configPath != "" {
		go watchConfig(*configPath, d, log)
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

// watchConfig watches the config file for changes and triggers a reload.
// It debounces rapid writes (e.g. atomic rename by Infisical agent) to
// avoid reloading multiple times for a single logical update.
func watchConfig(path string, d *daemon.Daemon, log *slog.Logger) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Error("failed to create config file watcher", "error", err)
		return
	}
	defer watcher.Close()

	if err := watcher.Add(path); err != nil {
		log.Error("failed to watch config file", "path", path, "error", err)
		return
	}

	log.Info("watching config file for changes", "path", path)

	var debounce *time.Timer
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				// Debounce: wait 500ms for writes to settle before reloading.
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(500*time.Millisecond, func() {
					log.Info("config file changed, reloading", "path", path)
					if err := d.Reload(path); err != nil {
						log.Error("config reload failed", "error", err)
					}
				})
			}
			// Re-watch on Create (atomic rename replaces the inode).
			if event.Has(fsnotify.Create) {
				_ = watcher.Remove(path)
				_ = watcher.Add(path)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Error("config file watcher error", "error", err)
		}
	}
}
