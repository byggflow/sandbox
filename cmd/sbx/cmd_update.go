package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const githubRepo = "byggflow/sandbox"

type ghRelease struct {
	TagName string `json:"tag_name"`
}

// fetchLatestVersion queries the GitHub API for the latest release tag.
func fetchLatestVersion(ctx context.Context) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("decoding release: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("empty tag_name in release response")
	}
	return rel.TagName, nil
}

// downloadAndReplace downloads the release tarball and replaces the current binary.
func downloadAndReplace(ctx context.Context, tag string) error {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	tarball := fmt.Sprintf("sandbox-%s-%s-%s.tar.gz", tag, goos, goarch)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, tag, tarball)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Extract sbx binary from the tarball.
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("decompressing tarball: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var sbxData []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tarball: %w", err)
		}
		if filepath.Base(hdr.Name) == "sbx" {
			sbxData, err = io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("reading sbx from tarball: %w", err)
			}
			break
		}
	}
	if sbxData == nil {
		return fmt.Errorf("sbx binary not found in tarball")
	}

	// Find the current binary path.
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolving symlinks: %w", err)
	}

	// Atomic replace: write to temp file next to the binary, then rename.
	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, ".sbx-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(sbxData); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}
	return nil
}

func runUpdate(args []string) int {
	fs := flag.NewFlagSet("sbx update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "Check for updates without installing")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("Checking for updates...")

	latest, err := fetchLatestVersion(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx update: %v\n", err)
		return 1
	}

	current := normalizeVersion(version)
	latestNorm := normalizeVersion(latest)

	if current == latestNorm {
		fmt.Printf("Already up to date (%s).\n", version)
		writeUpdateState(latest)
		return 0
	}

	fmt.Printf("Current: %s\n", version)
	fmt.Printf("Latest:  %s\n", latest)

	if *checkOnly {
		fmt.Println("\nRun 'sbx update' to install the latest version.")
		return 0
	}

	fmt.Printf("Downloading %s...\n", latest)
	if err := downloadAndReplace(ctx, latest); err != nil {
		fmt.Fprintf(os.Stderr, "sbx update: %v\n", err)
		return 1
	}

	writeUpdateState(latest)
	fmt.Printf("Updated to %s.\n", latest)
	return 0
}

// normalizeVersion strips a leading "v" for comparison.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}
