package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// checkInterval is how often the CLI checks for a new version in the background.
const checkInterval = 24 * time.Hour

type updateState struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

func stateDir() string {
	if d, err := os.UserHomeDir(); err == nil {
		return filepath.Join(d, ".sbx")
	}
	return ""
}

func stateFilePath() string {
	d := stateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "update-state.json")
}

func readUpdateState() *updateState {
	path := stateFilePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s updateState
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	return &s
}

func writeUpdateState(latestVersion string) {
	path := stateFilePath()
	if path == "" {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	s := updateState{
		LatestVersion: latestVersion,
		CheckedAt:     time.Now(),
	}
	data, _ := json.Marshal(s)
	os.WriteFile(path, data, 0o644)
}

// maybeCheckUpdate prints a nudge if a newer version is available.
// It runs a background HTTP check at most once per checkInterval.
// This is non-blocking: if the cached state is stale, it spawns a
// goroutine to refresh it and only nudges on the *next* invocation.
func maybeCheckUpdate() {
	if version == "0.0.0" || version == "dev" {
		return // Development build, skip.
	}

	state := readUpdateState()

	// If we have a recent check result, nudge immediately if needed.
	if state != nil && time.Since(state.CheckedAt) < checkInterval {
		latestNorm := normalizeVersion(state.LatestVersion)
		currentNorm := normalizeVersion(version)
		if latestNorm != "" && latestNorm != currentNorm {
			fmt.Fprintf(os.Stderr, "\nA new version of sbx is available: %s (current: %s)\n", state.LatestVersion, version)
			fmt.Fprintf(os.Stderr, "Run 'sbx update' to install it.\n\n")
		}
		return
	}

	// Stale or missing: refresh in the background for next time.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if latest, err := fetchLatestVersion(ctx); err == nil {
			writeUpdateState(latest)
		}
	}()
}
