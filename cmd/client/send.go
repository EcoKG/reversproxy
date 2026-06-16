package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/EcoKG/reversproxy/internal/filetransfer"
	"github.com/EcoKG/reversproxy/internal/logger"
)

// dispatchSubcommands runs a non-flag subcommand if present and exits the
// process; otherwise it returns and normal flag-based client startup continues.
//
// Usage:
//
//	reversproxy-client send [-to URL] [-token T] <file>...
//
// where -to is the peer's file-transfer receive endpoint as reachable from this
// host through the tunnel (e.g. a local port-forward or the server's public port).
func dispatchSubcommands() {
	if len(os.Args) < 2 || os.Args[1] != "send" {
		return
	}
	os.Exit(runSend(os.Args[2:]))
}

func runSend(args []string) int {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "http://127.0.0.1:8089", "peer file-transfer endpoint reachable through the tunnel (http://host:port)")
	token := fs.String("token", "", "upload token (must match the peer's file_transfer.token)")
	_ = fs.Parse(args)

	files := fs.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: reversproxy-client send [-to URL] [-token T] <file>...")
		return 2
	}

	log := logger.New("send")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rc := 0
	for _, f := range files {
		if err := filetransfer.SendFile(ctx, *to, f, filetransfer.SendOptions{Token: *token}, log); err != nil {
			fmt.Fprintf(os.Stderr, "send failed: %s: %v\n", f, err)
			rc = 1
			continue
		}
		fmt.Printf("sent: %s -> %s\n", f, *to)
	}
	return rc
}
