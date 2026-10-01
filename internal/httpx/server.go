// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package httpx is the HTTP surface: the dashboard, the health check and the
// MCP endpoint all hang off one mux so a number is served the same way
// whoever asks for it.
package httpx

import (
	"os"

	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/mcpserver"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/ui"
)

// Server is the HTTP listener and its dependencies.
type Server struct {
	db      *store.DB
	log     *slog.Logger
	version string
	http    *http.Server
}

// New builds a Server bound to addr. dash may be nil, which serves the health
// endpoint alone; that is the shape the tests use.
// run starts passes for the MCP endpoint's evaluation tools; nil leaves
// those tools unregistered, as in a test that has no runner.
func New(addr string, db *store.DB, log *slog.Logger, version string, dash *ui.App, run *runner.Runner) *Server {
	s := &Server{db: db, log: log, version: version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	if dash != nil {
		dash.Routes(mux)
	}

	// MCP over streamable HTTP, for a client that connects to a running
	// instance instead of launching one. It refuses every request until a
	// token is set, which is why it can be mounted unconditionally. The
	// token is read per request: the environment, else the one generated in
	// Settings, which is how a rotation takes effect without a restart.
	token := mcpserver.StaticToken(os.Getenv(mcpserver.TokenEnv))
	if dash != nil {
		token = dash.MCPToken
	}
	if srv, err := New_(db, mcpserver.DashboardURL(addr), run, dash); err == nil {
		mux.Handle("/mcp", mcpserver.Handler(srv, token, log))
		mux.Handle("/mcp/", mcpserver.Handler(srv, token, log))
	} else if log != nil {
		log.Error("the MCP endpoint could not be built", "error", err)
	}
	s.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Addr is the configured listen address.
func (s *Server) Addr() string { return s.http.Addr }

// Serve listens until ctx is cancelled, then drains in-flight requests.
// ready, when not nil, is called with the bound address once the port is
// open and before the first request, so nothing announces an address that
// failed to bind.
func (s *Server) Serve(ctx context.Context, ready func(net.Addr)) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	if ready != nil {
		ready(ln.Addr())
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	}
}

type health struct {
	Status     string   `json:"status"`
	Version    string   `json:"version"`
	Database   string   `json:"database"`
	Migrations []string `json:"migrations"`
}

// handleHealthz reports that the process is up AND that the database it
// depends on answers. A health check that only proves the listener is alive
// is the kind that stays green through an outage.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	body := health{Status: "ok", Version: s.version, Database: s.db.Path()}
	migrations, err := s.db.AppliedMigrations(ctx)
	if err != nil {
		s.log.Error("healthz database check failed", "error", err)
		body.Status = "degraded"
		writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	body.Migrations = migrations
	writeJSON(w, http.StatusOK, body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// New_ builds the MCP server for the HTTP endpoint. Named apart from New so
// the two constructors in this file cannot be confused at a glance. dashboard
// is this server's own address, which the setup block links to.
//
// The run tools get the same runner and the same ceiling as the dashboard's
// Run now, so a pass started from an agent is held to the limit in Settings.
func New_(db *store.DB, dashboard string, run *runner.Runner, dash *ui.App) (*mcp.Server, error) {
	deps := mcpserver.Deps{DB: db, DashboardURL: dashboard}
	if run != nil && dash != nil {
		deps.Runner, deps.RunsPerDay = run, dash.RunsPerDay
	}
	return mcpserver.New(deps)
}
