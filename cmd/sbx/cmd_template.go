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
	"text/tabwriter"
)

func runTplSave(args []string) int {
	fs := flag.NewFlagSet("tpl save", flag.ExitOnError)
	label := fs.String("label", "", "Template label")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: sbx tpl save <sandbox-id> [--label NAME]")
		return 1
	}

	sandboxID := remaining[0]
	ctx := context.Background()

	body := map[string]interface{}{
		"sandbox_id": sandboxID,
	}
	if *label != "" {
		body["label"] = *label
	}

	data, _ := json.Marshal(body)
	resp, err := doRequest(ctx, http.MethodPost, "/templates", bytes.NewReader(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx tpl save: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx tpl save: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	var info struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		fmt.Fprintf(os.Stderr, "sbx tpl save: decode response: %v\n", err)
		return 1
	}

	fmt.Println(info.ID)
	return 0
}

func runTplLs(args []string) int {
	ctx := context.Background()
	resp, err := doRequest(ctx, http.MethodGet, "/templates", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx tpl ls: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx tpl ls: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	var templates []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Image string `json:"image"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&templates); err != nil {
		fmt.Fprintf(os.Stderr, "sbx tpl ls: decode response: %v\n", err)
		return 1
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLABEL\tIMAGE")
	for _, t := range templates {
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.ID, t.Label, t.Image)
	}
	w.Flush()
	return 0
}

func runTplRm(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: sbx tpl rm <template-id>")
		return 1
	}

	id := args[0]
	ctx := context.Background()

	resp, err := doRequest(ctx, http.MethodDelete, "/templates/"+id, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx tpl rm: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx tpl rm: server error (status %d): %s\n", resp.StatusCode, string(respBody))
		return 1
	}

	fmt.Println(id)
	return 0
}
