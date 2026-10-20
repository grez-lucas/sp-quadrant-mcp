// Command sp-quadrant-mcp is an MCP server, speaking over stdio, that lets
// Claude Code list, create, move and complete Tasks on Super Productivity's
// Eisenhower board through SP desktop's Local REST API. See ADR-0001.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/grez-lucas/sp-quadrant-mcp/internal/sp"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/token"
	"github.com/grez-lucas/sp-quadrant-mcp/internal/tools"
)

func main() {
	// Stdout carries the MCP protocol; logs go to stderr only.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, config{
		tokenPath: token.DefaultPath,
		baseURL:   sp.DefaultBaseURL,
		transport: &mcp.StdioTransport{},
		logger:    logger,
	})
	stop()
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

type config struct {
	tokenPath func() (string, error)
	baseURL   string
	transport mcp.Transport
	logger    *slog.Logger
}

// run reads the token once, wires the tools to SP and serves until the client
// disconnects or ctx ends. A missing token does not stop the server: every
// tool call then explains how to enable the Local REST API.
func run(ctx context.Context, cfg config) error {
	tok := loadToken(cfg)
	desktop, err := sp.New(cfg.baseURL, tok)
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "sp-quadrant-mcp", Version: version()}, &mcp.ServerOptions{Logger: cfg.logger})
	tools.Register(server, desktop, cfg.logger)
	cfg.logger.Info("starting", "version", version(), "sp", cfg.baseURL, "token_loaded", tok != "")
	return server.Run(ctx, cfg.transport)
}

// loadToken returns the token, or "" when it cannot be read. It logs where it
// looked and why it failed, never the token.
func loadToken(cfg config) string {
	path, err := cfg.tokenPath()
	if err != nil {
		cfg.logger.Warn("cannot locate SP's Local REST API token; every tool call will fail until Claude Code restarts", "err", err)
		return ""
	}
	tok, err := token.Load(path)
	if err != nil {
		cfg.logger.Warn("cannot read SP's Local REST API token; every tool call will fail until Claude Code restarts",
			"path", path, "err", err)
		return ""
	}
	return tok
}

// version is the module version go install stamped into the binary.
func version() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
