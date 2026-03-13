// Package gateway provides a reusable loopback-only HTTP reverse proxy
// that captures request/response exchanges and invokes provider-agnostic
// hooks for adapter integration.
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// RequestCapture holds captured metadata and body from an incoming request.
type RequestCapture struct {
	RequestID  string
	Method     string
	URL        string
	Path       string
	Header     http.Header
	Body       []byte
	ReceivedAt time.Time
}

// ResponseCapture holds captured metadata and body from the upstream response.
type ResponseCapture struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Duration   time.Duration // time from request received to response complete
	ReceivedAt time.Time
}

// Exchange pairs a captured request with its upstream response under a
// shared correlation ID.
type Exchange struct {
	RequestID string
	Request   *RequestCapture
	Response  *ResponseCapture
}

// RequestHook is called after capturing an incoming request, before
// forwarding upstream. Adapters use this to inspect or log requests.
type RequestHook func(ctx context.Context, req *RequestCapture)

// ResponseHook is called after capturing the upstream response, before
// replying to the caller. Adapters use this to emit normalized events.
type ResponseHook func(ctx context.Context, exchange *Exchange)

// Config configures a Gateway instance.
type Config struct {
	ListenPort  int          // port on 127.0.0.1; 0 for OS-assigned
	UpstreamURL string       // base URL to forward requests to
	OnRequest   RequestHook  // optional; called before forwarding
	OnResponse  ResponseHook // optional; called after upstream responds
	Client      *http.Client // optional; defaults to http.DefaultClient
}

// Gateway is a loopback-only HTTP reverse proxy that captures
// request/response exchanges and invokes hooks for adapter integration.
type Gateway struct {
	cfg      Config
	server   *http.Server
	listener net.Listener
	client   *http.Client
}

// New creates a Gateway bound to 127.0.0.1. The listener is opened
// immediately so Addr/Port are available before Start is called.
func New(cfg Config) (*Gateway, error) {
	if cfg.UpstreamURL == "" {
		return nil, fmt.Errorf("gateway: UpstreamURL is required")
	}
	cfg.UpstreamURL = strings.TrimRight(cfg.UpstreamURL, "/")

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.ListenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("gateway: listen %s: %w", addr, err)
	}

	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}

	g := &Gateway{
		cfg:      cfg,
		listener: ln,
		client:   client,
	}
	g.server = &http.Server{Handler: http.HandlerFunc(g.handle)}
	return g, nil
}

// Addr returns the listener address (e.g. "127.0.0.1:9200").
func (g *Gateway) Addr() string {
	return g.listener.Addr().String()
}

// Port returns the actual listening port (useful when ListenPort was 0).
func (g *Gateway) Port() int {
	return g.listener.Addr().(*net.TCPAddr).Port
}

// Start begins serving requests. It blocks until the server shuts down
// and returns http.ErrServerClosed on graceful shutdown.
func (g *Gateway) Start() error {
	return g.server.Serve(g.listener)
}

// Stop gracefully shuts down the gateway.
func (g *Gateway) Stop(ctx context.Context) error {
	return g.server.Shutdown(ctx)
}

func (g *Gateway) handle(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now()
	reqID := GenerateID()

	// Capture request body.
	var reqBody []byte
	if r.Body != nil {
		var err error
		reqBody, err = io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "gateway: failed to read request body", http.StatusBadGateway)
			return
		}
	}

	capture := &RequestCapture{
		RequestID:  reqID,
		Method:     r.Method,
		URL:        r.URL.String(),
		Path:       r.URL.Path,
		Header:     r.Header.Clone(),
		Body:       reqBody,
		ReceivedAt: receivedAt,
	}

	if g.cfg.OnRequest != nil {
		g.cfg.OnRequest(r.Context(), capture)
	}

	// Build upstream request.
	upstreamURL := g.cfg.UpstreamURL + r.URL.RequestURI()
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "gateway: failed to create upstream request", http.StatusBadGateway)
		return
	}
	for k, vv := range r.Header {
		for _, v := range vv {
			upReq.Header.Add(k, v)
		}
	}

	// Forward to upstream.
	resp, err := g.client.Do(upReq)
	if err != nil {
		http.Error(w, "gateway: upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "gateway: failed to read upstream response", http.StatusBadGateway)
		return
	}

	now := time.Now()
	exchange := &Exchange{
		RequestID: reqID,
		Request:   capture,
		Response: &ResponseCapture{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       respBody,
			Duration:   now.Sub(receivedAt),
			ReceivedAt: now,
		},
	}

	if g.cfg.OnResponse != nil {
		g.cfg.OnResponse(r.Context(), exchange)
	}

	// Write upstream response back to the caller.
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// GenerateID returns a unique correlation ID with a "gw-" prefix.
func GenerateID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("gw-%d", time.Now().UnixNano())
	}
	return "gw-" + hex.EncodeToString(b)
}
