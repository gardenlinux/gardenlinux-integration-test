package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	rootCmd := &cobra.Command{
		Use:     "integrationtest",
		Short:   "Garden Linux integration test",
		Version: version,
	}

	rootCmd.AddCommand(publishCmd(), unpublishCmd())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}

	return 0
}
