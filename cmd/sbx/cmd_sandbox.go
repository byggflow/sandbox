package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func runCreate(args []string) int {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	profile := fs.String("profile", "", "Pool profile name")
	template := fs.String("template", "", "Template ID to create from")
	memory := fs.String("memory", "", "Memory limit (e.g. 512m)")
	cpu := fs.Float64("cpu", 0, "CPU limit")
	ttl := fs.Int("ttl", 0, "Time-to-live in seconds")
	var labels labelFlag
	fs.Var(&labels, "l", "Label in key=value format (repeatable)")
	fs.Parse(args)

	body := map[string]interface{}{}
	if *profile != "" {
		body["profile"] = *profile
	}
	if *template != "" {
		body["template"] = *template
	}
	if *memory != "" {
		body["memory"] = *memory
	}
	if *cpu > 0 {
		body["cpu"] = *cpu
	}
	if *ttl > 0 {
		body["ttl"] = *ttl
	}
	if len(labels) > 0 {
		labelMap := map[string]string{}
		for _, l := range labels {
			parts := strings.SplitN(l, "=", 2)
			labelMap[parts[0]] = parts[1]
		}
		body["labels"] = labelMap
	}

	data, _ := json.Marshal(body)
	ctx := context.Background()
	resp, err := doRequest(ctx, http.MethodPost, "/sandboxes", bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx create: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx create: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	var info struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		fmt.Fprintf(os.Stderr, "sbx create: decode response: %v\n", err)
		return 1
	}

	fmt.Println(info.ID)
	return 0
}

func runLs(args []string) int {
	ctx := context.Background()
	resp, err := doRequest(ctx, http.MethodGet, "/sandboxes", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx ls: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx ls: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	var sandboxes []struct {
		ID      string `json:"id"`
		Image   string `json:"image"`
		State   string `json:"state"`
		Created string `json:"created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sandboxes); err != nil {
		fmt.Fprintf(os.Stderr, "sbx ls: decode response: %v\n", err)
		return 1
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tIMAGE\tSTATUS\tCREATED")
	for _, s := range sandboxes {
		created := s.Created
		if t, err := time.Parse(time.RFC3339Nano, s.Created); err == nil {
			created = t.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ID, s.Image, s.State, created)
	}
	w.Flush()
	return 0
}

func runRm(args []string) int {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	all := fs.Bool("all", false, "Remove all sandboxes")
	fs.Parse(args)

	ctx := context.Background()

	if *all {
		// List all sandboxes, then delete each.
		resp, err := doRequest(ctx, http.MethodGet, "/sandboxes", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sbx rm: %v\n", err)
			return 1
		}
		defer resp.Body.Close()

		var sandboxes []struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&sandboxes); err != nil {
			fmt.Fprintf(os.Stderr, "sbx rm: decode response: %v\n", err)
			return 1
		}

		for _, s := range sandboxes {
			delResp, err := doRequest(ctx, http.MethodDelete, "/sandboxes/"+s.ID, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "sbx rm: %s: %v\n", s.ID, err)
				continue
			}
			delResp.Body.Close()
			fmt.Println(s.ID)
		}
		return 0
	}

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "sbx rm: sandbox ID required (or --all)")
		return 1
	}

	id := remaining[0]
	resp, err := doRequest(ctx, http.MethodDelete, "/sandboxes/"+id, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx rm: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx rm: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	fmt.Println(id)
	return 0
}

func runStats(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: sbx stats <sandbox-id>")
		return 1
	}

	id := args[0]
	ctx := context.Background()

	resp, err := doRequest(ctx, http.MethodGet, "/sandboxes/"+id+"/stats", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx stats: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx stats: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	var stats struct {
		CPUPercent       float64 `json:"cpu_percent"`
		MemoryUsageBytes uint64  `json:"memory_usage_bytes"`
		MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
		MemoryPercent    float64 `json:"memory_percent"`
		NetworkRxBytes   uint64  `json:"network_rx_bytes"`
		NetworkTxBytes   uint64  `json:"network_tx_bytes"`
		PIDs             uint64  `json:"pids"`
		UptimeSeconds    float64 `json:"uptime_seconds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		fmt.Fprintf(os.Stderr, "sbx stats: decode response: %v\n", err)
		return 1
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "CPU:\t%.2f%%\n", stats.CPUPercent)
	fmt.Fprintf(w, "Memory:\t%s / %s (%.1f%%)\n",
		formatBytes(stats.MemoryUsageBytes), formatBytes(stats.MemoryLimitBytes), stats.MemoryPercent)
	fmt.Fprintf(w, "Network RX:\t%s\n", formatBytes(stats.NetworkRxBytes))
	fmt.Fprintf(w, "Network TX:\t%s\n", formatBytes(stats.NetworkTxBytes))
	fmt.Fprintf(w, "PIDs:\t%d\n", stats.PIDs)
	fmt.Fprintf(w, "Uptime:\t%s\n", formatDuration(stats.UptimeSeconds))
	w.Flush()
	return 0
}
