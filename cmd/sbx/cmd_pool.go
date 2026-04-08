package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func runPoolStatus(args []string) int {
	ctx := context.Background()
	resp, err := doRequest(ctx, http.MethodGet, "/pools", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx pool status: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx pool status: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	var data json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "sbx pool status: decode response: %v\n", err)
		return 1
	}

	// Pretty print.
	var out bytes.Buffer
	json.Indent(&out, data, "", "  ")
	fmt.Println(out.String())
	return 0
}

func runPoolResize(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sbx pool resize <profile> <count>")
		return 1
	}

	profile := args[0]
	var count int
	if _, err := fmt.Sscanf(args[1], "%d", &count); err != nil {
		fmt.Fprintf(os.Stderr, "sbx pool resize: invalid count %q\n", args[1])
		return 1
	}

	ctx := context.Background()
	body := map[string]interface{}{"count": count}
	data, _ := json.Marshal(body)
	resp, err := doRequest(ctx, http.MethodPut, "/pools/"+profile, bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx pool resize: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx pool resize: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	fmt.Println("ok")
	return 0
}

func runPoolFlush(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: sbx pool flush <profile>")
		return 1
	}

	profile := args[0]
	ctx := context.Background()

	resp, err := doRequest(ctx, http.MethodPost, "/pools/"+profile+"/flush", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx pool flush: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx pool flush: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	fmt.Println("ok")
	return 0
}

func runHealth(args []string) int {
	ctx := context.Background()
	resp, err := doRequest(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx health: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx health: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	var data json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Fprintf(os.Stderr, "sbx health: decode response: %v\n", err)
		return 1
	}

	var out bytes.Buffer
	json.Indent(&out, data, "", "  ")
	fmt.Println(out.String())
	return 0
}

func runVersion(args []string) int {
	fmt.Printf("sbx version %s\n", version)

	// Try to get server version from /health.
	ctx := context.Background()
	resp, err := doRequest(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return 0 // Server unreachable — just print client version.
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var health struct {
			Version string `json:"version"`
			Status  string `json:"status"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&health); err == nil {
			if health.Version != "" {
				fmt.Printf("sandboxd version %s\n", health.Version)
			}
			if health.Status != "" {
				fmt.Printf("status: %s\n", health.Status)
			}
		}
	}
	return 0
}
