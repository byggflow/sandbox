package main

import (
	"fmt"
	"os"
)

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
	case "stats":
		os.Exit(runStats(args))
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
