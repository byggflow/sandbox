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
	"os/exec"
	"time"
)

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	dockerfile := fs.String("f", "Dockerfile", "Path to Dockerfile")
	templateLabel := fs.String("t", "", "Template label")
	fs.Parse(args)

	contextDir := "."
	if remaining := fs.Args(); len(remaining) > 0 {
		contextDir = remaining[0]
	}

	if *templateLabel == "" {
		fmt.Fprintln(os.Stderr, "Usage: sbx build -f Dockerfile -t my-template [context-dir]")
		fmt.Fprintln(os.Stderr, "  -t is required (template label)")
		return 1
	}

	ctx := context.Background()

	// Step 1: Build the Docker image with a temporary tag.
	tempTag := "sbx-build-" + fmt.Sprintf("%d", time.Now().UnixNano())
	fmt.Fprintf(os.Stderr, "Building image from %s...\n", *dockerfile)

	buildCmd := exec.CommandContext(ctx, "docker", "build", "-t", tempTag, "-f", *dockerfile, contextDir)
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "sbx build: docker build failed: %v\n", err)
		return 1
	}

	// Step 2: Verify the built image has sandbox-agent.
	fmt.Fprintln(os.Stderr, "Verifying sandbox-agent is present in image...")
	verifyCmd := exec.CommandContext(ctx, "docker", "run", "--rm", tempTag, "which", "sandbox-agent")
	verifyOut, err := verifyCmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx build: image missing /usr/local/bin/sandbox-agent: %v\n%s\n", err, string(verifyOut))
		// Clean up the temporary image.
		exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()
		return 1
	}
	fmt.Fprintf(os.Stderr, "Agent found at %s", string(verifyOut))

	// Step 3: Create a sandbox from the built image.
	fmt.Fprintln(os.Stderr, "Creating sandbox from image...")
	createBody := map[string]interface{}{
		"image": tempTag,
	}
	createData, _ := json.Marshal(createBody)
	resp, err := doRequest(ctx, http.MethodPost, "/sandboxes", bytes.NewReader(createData))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx build: create sandbox: %v\n", err)
		exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "sbx build: create sandbox failed (status %d): %s\n", resp.StatusCode, string(respBody))
		exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()
		return 1
	}

	var sbxInfo struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sbxInfo); err != nil {
		fmt.Fprintf(os.Stderr, "sbx build: decode sandbox response: %v\n", err)
		exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()
		return 1
	}

	// Step 4: Snapshot the sandbox as a template.
	fmt.Fprintln(os.Stderr, "Snapshotting sandbox as template...")
	tplBody := map[string]interface{}{
		"sandbox_id": sbxInfo.ID,
		"label":      *templateLabel,
	}
	tplData, _ := json.Marshal(tplBody)
	tplResp, err := doRequest(ctx, http.MethodPost, "/templates", bytes.NewReader(tplData))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx build: create template: %v\n", err)
		// Clean up: destroy sandbox, remove image.
		doRequest(ctx, http.MethodDelete, "/sandboxes/"+sbxInfo.ID, nil)
		exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()
		return 1
	}
	defer tplResp.Body.Close()

	if tplResp.StatusCode != http.StatusCreated && tplResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(tplResp.Body)
		fmt.Fprintf(os.Stderr, "sbx build: create template failed (status %d): %s\n", tplResp.StatusCode, string(respBody))
		doRequest(ctx, http.MethodDelete, "/sandboxes/"+sbxInfo.ID, nil)
		exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()
		return 1
	}

	var tplInfo struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(tplResp.Body).Decode(&tplInfo); err != nil {
		fmt.Fprintf(os.Stderr, "sbx build: decode template response: %v\n", err)
		doRequest(ctx, http.MethodDelete, "/sandboxes/"+sbxInfo.ID, nil)
		exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()
		return 1
	}

	// Step 5: Destroy the temporary sandbox.
	delResp, err := doRequest(ctx, http.MethodDelete, "/sandboxes/"+sbxInfo.ID, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx build: warning: failed to destroy temp sandbox %s: %v\n", sbxInfo.ID, err)
	} else {
		delResp.Body.Close()
	}

	// Clean up the temporary build image (the template has its own committed image).
	exec.CommandContext(ctx, "docker", "rmi", tempTag).Run()

	fmt.Fprintln(os.Stderr, "Template created successfully.")
	fmt.Println(tplInfo.ID)
	return 0
}
