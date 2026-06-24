package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aleksclark/bezalel/internal/server"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	workDir := flag.String("workdir", "", "working directory (defaults to cwd)")
	flag.Parse()

	if *workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Error("failed to get working directory", "error", err)
			os.Exit(1)
		}
		*workDir = wd
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	srv := server.New(*workDir)

	httpServer := &http.Server{
		Addr:         *addr,
		Handler:      srv,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	go func() {
		slog.Info("bezalel starting", "addr", *addr, "workdir", *workDir)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	srv.Shutdown()
	fmt.Println("bezalel stopped")
}
