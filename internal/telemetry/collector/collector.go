package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

const defaultAddr = "127.0.0.1:0"

type Request struct {
	Method     string
	Path       string
	Headers    http.Header
	Body       []byte
	RemoteAddr string
	ReceivedAt time.Time
}

type Handler func(context.Context, Request) error

type Options struct {
	Addr    string
	Logger  *slog.Logger
	Handler Handler
}

type Server struct {
	addr     string
	endpoint string
	logger   *slog.Logger
	listener net.Listener
	server   *http.Server
}

func Start(options Options) (*Server, error) {
	if options.Handler == nil {
		return nil, errors.New("collector handler must not be nil")
	}

	addr := strings.TrimSpace(options.Addr)
	if addr == "" {
		addr = defaultAddr
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen otlp collector on %s: %w", addr, err)
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	srv := &Server{
		addr:     listener.Addr().String(),
		endpoint: "http://" + listener.Addr().String(),
		logger:   logger,
		listener: listener,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handle(options.Handler))
	srv.server = &http.Server{
		Handler: mux,
	}

	go func() {
		err := srv.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("otlp collector serve failed", "error", err)
		}
	}()

	return srv, nil
}

func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

func (s *Server) Endpoint() string {
	if s == nil {
		return ""
	}
	return s.endpoint
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) handle(handler Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"error": "method not allowed",
			})
			return
		}

		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/v1/") {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error": "unsupported otlp path",
			})
			return
		}

		if !isLoopbackRemote(r.RemoteAddr) {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": "collector accepts loopback requests only",
			})
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "read request body",
			})
			return
		}
		defer func() { _ = r.Body.Close() }()

		req := Request{
			Method:     r.Method,
			Path:       r.URL.Path,
			Headers:    r.Header.Clone(),
			Body:       body,
			RemoteAddr: r.RemoteAddr,
			ReceivedAt: time.Now().UTC(),
		}

		if err := handler(r.Context(), req); err != nil {
			s.logger.Warn("collector request failed", "path", r.URL.Path, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "collector handler failed",
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{})
	}
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
