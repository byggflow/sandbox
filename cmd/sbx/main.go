package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	sandbox "github.com/byggflow/sandbox/sdk/go"
	"golang.org/x/term"
)

const version = "0.0.1"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "build":
		os.Exit(runBuild(args))
	case "create":
		os.Exit(runCreate(args))
	case "ls":
		os.Exit(runLs(args))
	case "rm":
		os.Exit(runRm(args))
	case "exec":
		os.Exit(runExec(args))
	case "attach":
		os.Exit(runAttach(args))
	case "fs":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "sbx fs: subcommand required (read, write, ls, upload, download)")
			os.Exit(1)
		}
		subcmd := args[0]
		subargs := args[1:]
		switch subcmd {
		case "read":
			os.Exit(runFsRead(subargs))
		case "write":
			os.Exit(runFsWrite(subargs))
		case "ls":
			os.Exit(runFsLs(subargs))
		case "upload":
			os.Exit(runFsUpload(subargs))
		case "download":
			os.Exit(runFsDownload(subargs))
		default:
			fmt.Fprintf(os.Stderr, "sbx fs: unknown subcommand %q\n", subcmd)
			os.Exit(1)
		}
	case "tpl":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "sbx tpl: subcommand required (save, ls, rm)")
			os.Exit(1)
		}
		subcmd := args[0]
		subargs := args[1:]
		switch subcmd {
		case "save":
			os.Exit(runTplSave(subargs))
		case "ls":
			os.Exit(runTplLs(subargs))
		case "rm":
			os.Exit(runTplRm(subargs))
		default:
			fmt.Fprintf(os.Stderr, "sbx tpl: unknown subcommand %q\n", subcmd)
			os.Exit(1)
		}
	case "pool":
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "sbx pool: subcommand required (status, resize, flush)")
			os.Exit(1)
		}
		subcmd := args[0]
		subargs := args[1:]
		switch subcmd {
		case "status":
			os.Exit(runPoolStatus(subargs))
		case "resize":
			os.Exit(runPoolResize(subargs))
		case "flush":
			os.Exit(runPoolFlush(subargs))
		default:
			fmt.Fprintf(os.Stderr, "sbx pool: unknown subcommand %q\n", subcmd)
			os.Exit(1)
		}
	case "health":
		os.Exit(runHealth(args))
	case "version":
		os.Exit(runVersion(args))
	case "help", "--help", "-h":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "sbx: unknown command %q\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: sbx <command> [options]

Commands:
  build       Build a template from a Dockerfile
  create      Create a new sandbox
  ls          List sandboxes
  rm          Remove a sandbox
  exec        Execute a command in a sandbox
  attach      Attach to a sandbox with a PTY
  fs          Filesystem operations (read, write, ls, upload, download)
  tpl         Template operations (save, ls, rm)
  pool        Pool operations (status, resize, flush)
  health      Check daemon health
  version     Print version information

Environment:
  SANDBOXD_ENDPOINT  Daemon endpoint (default: http://localhost:7522)
  SBX_AUTH           Authentication token`)
}

// endpoint returns the daemon endpoint from env or default.
func endpoint() string {
	if e := os.Getenv("SANDBOXD_ENDPOINT"); e != "" {
		return e
	}
	return "http://localhost:7522"
}

// authFromEnv returns an Auth from SBX_AUTH env var, or nil.
func authFromEnv() sandbox.Auth {
	if tok := os.Getenv("SBX_AUTH"); tok != "" {
		return &sandbox.StringAuth{Token: tok}
	}
	return nil
}

// httpClient returns an HTTP client and base URL for the endpoint.
func httpClient() (*http.Client, string) {
	ep := endpoint()
	if strings.HasPrefix(ep, "unix://") {
		sockPath := strings.TrimPrefix(ep, "unix://")
		return &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
				},
			},
		}, "http://localhost"
	}
	return http.DefaultClient, ep
}

// doRequest performs an HTTP request with auth headers.
func doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	client, baseURL := httpClient()
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := os.Getenv("SBX_AUTH"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return client.Do(req)
}

// connectSDK connects to a sandbox via the SDK.
func connectSDK(ctx context.Context, id string) (*sandbox.Sandbox, error) {
	return sandbox.Connect(ctx, id, &sandbox.ConnectOptions{
		Endpoint: endpoint(),
		Auth:     authFromEnv(),
	})
}

func runCreate(args []string) int {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	profile := fs.String("profile", "", "Pool profile name")
	template := fs.String("template", "", "Template ID to create from")
	memory := fs.String("memory", "", "Memory limit (e.g. 512m)")
	cpu := fs.Float64("cpu", 0, "CPU limit")
	ttl := fs.Int("ttl", 0, "Time-to-live in seconds")
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

func runExec(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sbx exec <id> <command...>")
		return 1
	}

	id := args[0]
	command := strings.Join(args[1:], " ")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	sbx, err := connectSDK(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx exec: connect: %v\n", err)
		return 1
	}
	defer sbx.Close()

	result, err := sbx.Process().Exec(ctx, command, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx exec: %v\n", err)
		return 1
	}

	if result.Stdout != "" {
		fmt.Fprint(os.Stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}

	return result.ExitCode
}

func runAttach(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: sbx attach <id>")
		return 1
	}

	id := args[0]

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	sbx, err := connectSDK(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx attach: connect: %v\n", err)
		return 1
	}
	defer sbx.Close()

	// Get terminal size.
	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		cols, rows = w, h
	}

	// Allocate PTY.
	pty, err := sbx.Process().Pty(ctx, &sandbox.PtyOptions{
		Cols: cols,
		Rows: rows,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx attach: pty: %v\n", err)
		return 1
	}

	// Put terminal in raw mode.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx attach: raw mode: %v\n", err)
		return 1
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Handle SIGWINCH for terminal resize.
	sigwinch := make(chan os.Signal, 1)
	signal.Notify(sigwinch, syscall.SIGWINCH)
	go func() {
		for range sigwinch {
			if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
				pty.Resize(ctx, w, h)
			}
		}
	}()

	// Read stdin and send to PTY.
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				cancel()
				return
			}
			if err := pty.Write(ctx, buf[:n]); err != nil {
				cancel()
				return
			}
		}
	}()

	// Wait for PTY to exit.
	exitCode, err := pty.Wait(ctx)
	if err != nil {
		// Ignore errors on context cancel (user pressed Ctrl+C).
		if ctx.Err() != nil {
			return 0
		}
		fmt.Fprintf(os.Stderr, "\r\nsbx attach: %v\n", err)
		return 1
	}

	return exitCode
}

func runFsRead(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sbx fs read <id> <path>")
		return 1
	}

	id, path := args[0], args[1]
	ctx := context.Background()

	sbx, err := connectSDK(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs read: connect: %v\n", err)
		return 1
	}
	defer sbx.Close()

	data, err := sbx.FS().Read(ctx, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs read: %v\n", err)
		return 1
	}

	os.Stdout.Write(data)
	return 0
}

func runFsWrite(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sbx fs write <id> <path>")
		return 1
	}

	id, path := args[0], args[1]
	ctx := context.Background()

	sbx, err := connectSDK(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs write: connect: %v\n", err)
		return 1
	}
	defer sbx.Close()

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs write: read stdin: %v\n", err)
		return 1
	}

	if err := sbx.FS().Write(ctx, path, data); err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs write: %v\n", err)
		return 1
	}

	return 0
}

func runFsLs(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sbx fs ls <id> <path>")
		return 1
	}

	id, path := args[0], args[1]
	ctx := context.Background()

	sbx, err := connectSDK(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs ls: connect: %v\n", err)
		return 1
	}
	defer sbx.Close()

	entries, err := sbx.FS().List(ctx, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs ls: %v\n", err)
		return 1
	}

	for _, entry := range entries {
		fmt.Println(entry)
	}
	return 0
}

func runFsUpload(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sbx fs upload <id> <path>")
		return 1
	}

	id, path := args[0], args[1]
	ctx := context.Background()

	sbx, err := connectSDK(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs upload: connect: %v\n", err)
		return 1
	}
	defer sbx.Close()

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs upload: read stdin: %v\n", err)
		return 1
	}

	if err := sbx.FS().Upload(ctx, path, data); err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs upload: %v\n", err)
		return 1
	}

	return 0
}

func runFsDownload(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: sbx fs download <id> <path>")
		return 1
	}

	id, path := args[0], args[1]
	ctx := context.Background()

	sbx, err := connectSDK(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs download: connect: %v\n", err)
		return 1
	}
	defer sbx.Close()

	data, err := sbx.FS().Download(ctx, path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sbx fs download: %v\n", err)
		return 1
	}

	os.Stdout.Write(data)
	return 0
}

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
