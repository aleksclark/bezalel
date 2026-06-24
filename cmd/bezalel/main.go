package main

import (
	"log/slog"
	"os"

	"github.com/aleksclark/bezalel/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}
