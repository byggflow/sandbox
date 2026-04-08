package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

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
