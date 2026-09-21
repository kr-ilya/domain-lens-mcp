// Package mcpserver exposes the availability engine as MCP tools over stdio
// or Streamable HTTP.
package mcpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kr-ilya/domain-lens-mcp/internal/engine"
)

// Server wires the engine into an MCP server instance.
type Server struct {
	mcp    *mcp.Server
	engine *engine.Engine
	logger *slog.Logger
}

// New builds an MCP server exposing the domain availability tools.
func New(eng *engine.Engine, version string, logger *slog.Logger) *Server {
	impl := &mcp.Implementation{
		Name:    "domain-lens-mcp",
		Title:   "Domain Lens",
		Version: version,
	}
	options := &mcp.ServerOptions{
		Instructions: "Domain availability intelligence. Check whether domains can be registered, " +
			"inspect registration details, and expand your own name ideas into verified candidates. " +
			"Results distinguish available, unavailable and unknown: unknown means no source could answer, " +
			"so never present it to the user as a free domain.",
	}

	s := &Server{mcp: mcp.NewServer(impl, options), engine: eng, logger: logger}
	s.registerTools()
	return s
}

// RunStdio serves MCP over stdin/stdout. Nothing else may write to stdout.
func (s *Server) RunStdio(ctx context.Context) error {
	s.logger.Info("serving mcp over stdio")
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// RunHTTP serves MCP over Streamable HTTP, plus a health endpoint for Docker.
func (s *Server) RunHTTP(ctx context.Context, addr string) error {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		s.logger.Info("serving mcp over streamable http", "addr", addr, "endpoint", "/mcp")
		errs <- server.ListenAndServe()
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
