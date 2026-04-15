package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	sandbox "github.com/byggflow/sandbox/sdk/go"
	"golang.org/x/term"
)

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
	fs := flag.NewFlagSet("sbx attach", flag.ContinueOnError)
	shell := fs.String("shell", "", "Shell to launch (e.g. /bin/bash)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: sbx attach [--shell <path>] <id>")
		return 1
	}

	id := fs.Arg(0)

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
		Command: *shell,
		Cols:    cols,
		Rows:    rows,
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
