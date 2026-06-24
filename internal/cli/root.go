// Package cli wires the bezalel command-line interface using cobra and viper.
// Every flag can be supplied via CLI flag, environment variable (BEZALEL_*),
// or a config file (bezalel.yaml/json/toml), in that order of precedence.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/aleksclark/bezalel/internal/lsp"
	"github.com/aleksclark/bezalel/internal/server"
	"github.com/aleksclark/bezalel/internal/version"
)

// Version is the bezalel binary version, sourced from internal/version.
var Version = version.Number

// envPrefix is the prefix for environment-variable configuration,
// e.g. --auth-token maps to BEZALEL_AUTH_TOKEN.
const envPrefix = "BEZALEL"

var cfgFile string

// NewRootCmd builds the root cobra command.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "bezalel",
		Short:         "MCP server sidecar providing shell and filesystem tools",
		Long:          "Bezalel is an MCP (Model Context Protocol) server exposing shell execution,\nbackground job management, and filesystem operations over Streamable HTTP.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          run,
	}

	flags := cmd.Flags()
	flags.StringVar(&cfgFile, "config", "", "config file (default: search ./bezalel.yaml, $HOME/.config/bezalel/bezalel.yaml, /etc/bezalel/bezalel.yaml)")
	flags.String("host", "", "host/interface to bind (empty = all interfaces)")
	flags.Int("port", 8080, "port to listen on")
	flags.String("workdir", "", "working directory for tool execution (defaults to current directory)")
	flags.String("auth-token", "", "bearer token required on /mcp requests (auth disabled if empty)")

	// Bind every flag (except --config, which targets cfgFile directly) to
	// viper so config-file and env values are honored.
	flags.VisitAll(func(f *pflag.Flag) {
		if f.Name == "config" {
			return
		}
		if err := viper.BindPFlag(f.Name, f); err != nil {
			panic(fmt.Sprintf("bind flag %q: %v", f.Name, err))
		}
	})

	cobra.OnInitialize(initConfig)
	return cmd
}

// Execute runs the root command.
func Execute() error {
	return NewRootCmd().Execute()
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("bezalel")
		viper.AddConfigPath(".")
		if home, err := os.UserHomeDir(); err == nil {
			viper.AddConfigPath(home + "/.config/bezalel")
		}
		viper.AddConfigPath("/etc/bezalel")
	}

	viper.SetEnvPrefix(envPrefix)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			slog.Warn("failed to read config file", "error", err)
		}
	}
}

func run(cmd *cobra.Command, _ []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	workDir := viper.GetString("workdir")
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		workDir = wd
	}

	addr := net.JoinHostPort(viper.GetString("host"), strconv.Itoa(viper.GetInt("port")))
	authToken := viper.GetString("auth-token")

	var lspServers []lsp.ServerConfig
	if err := viper.UnmarshalKey("lsp", &lspServers); err != nil {
		slog.Warn("failed to parse lsp config", "error", err)
	}

	srv := server.NewWithOptions(server.Options{
		WorkingDir: workDir,
		AuthToken:  authToken,
		LSPServers: lspServers,
	})

	if !srv.AuthEnabled() {
		slog.Warn("no auth token configured — /mcp is publicly accessible; set --auth-token or BEZALEL_AUTH_TOKEN")
	}

	if len(lspServers) > 0 {
		names := make([]string, 0, len(lspServers))
		for _, s := range lspServers {
			names = append(names, s.Name)
		}
		slog.Info("language servers configured", "servers", strings.Join(names, ","))
	}

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv,
		ReadTimeout:  5 * time.Minute,
		WriteTimeout: 5 * time.Minute,
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("bezalel starting", "addr", addr, "workdir", workDir, "auth", srv.AuthEnabled())
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		slog.Info("shutting down...")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	srv.Shutdown()
	slog.Info("bezalel stopped")
	return nil
}
