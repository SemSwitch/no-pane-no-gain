package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestForwardGetRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "true")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()

	gw, err := New(Config{UpstreamURL: upstream.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go gw.Start()
	defer gw.Stop(context.Background())

	resp, err := http.Get(fmt.Sprintf("http://%s/test", gw.Addr()))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello from upstream" {
		t.Errorf("body = %q, want %q", body, "hello from upstream")
	}
	if resp.Header.Get("X-Upstream") != "true" {
		t.Error("missing X-Upstream response header")
	}
}

func TestForwardPostWithBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "echo: %s", body)
	}))
	defer upstream.Close()

	gw, err := New(Config{UpstreamURL: upstream.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go gw.Start()
	defer gw.Stop(context.Background())

	resp, err := http.Post(
		fmt.Sprintf("http://%s/api/chat", gw.Addr()),
		"application/json",
		strings.NewReader(`{"prompt":"hi"}`),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `echo: {"prompt":"hi"}` {
		t.Errorf("body = %q", body)
	}
}

func TestHooksCalledWithCapturedData(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	var mu sync.Mutex
	var capturedReq *RequestCapture
	var capturedExchange *Exchange

	gw, err := New(Config{
		UpstreamURL: upstream.URL,
		OnRequest: func(ctx context.Context, req *RequestCapture) {
			mu.Lock()
			defer mu.Unlock()
			capturedReq = req
		},
		OnResponse: func(ctx context.Context, ex *Exchange) {
			mu.Lock()
			defer mu.Unlock()
			capturedExchange = ex
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go gw.Start()
	defer gw.Stop(context.Background())

	resp, err := http.Post(
		fmt.Sprintf("http://%s/v1/messages", gw.Addr()),
		"application/json",
		strings.NewReader(`{"role":"user"}`),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()

	// Verify request capture.
	if capturedReq == nil {
		t.Fatal("OnRequest not called")
	}
	if capturedReq.Method != "POST" {
		t.Errorf("req.Method = %q, want POST", capturedReq.Method)
	}
	if capturedReq.Path != "/v1/messages" {
		t.Errorf("req.Path = %q, want /v1/messages", capturedReq.Path)
	}
	if string(capturedReq.Body) != `{"role":"user"}` {
		t.Errorf("req.Body = %q", capturedReq.Body)
	}
	if capturedReq.RequestID == "" {
		t.Error("req.RequestID is empty")
	}
	if !strings.HasPrefix(capturedReq.RequestID, "gw-") {
		t.Errorf("req.RequestID = %q, want gw- prefix", capturedReq.RequestID)
	}
	if capturedReq.ReceivedAt.IsZero() {
		t.Error("req.ReceivedAt is zero")
	}

	// Verify exchange capture.
	if capturedExchange == nil {
		t.Fatal("OnResponse not called")
	}
	if capturedExchange.RequestID != capturedReq.RequestID {
		t.Errorf("exchange.RequestID = %q, want %q", capturedExchange.RequestID, capturedReq.RequestID)
	}
	if capturedExchange.Response.StatusCode != 200 {
		t.Errorf("resp.StatusCode = %d, want 200", capturedExchange.Response.StatusCode)
	}
	if string(capturedExchange.Response.Body) != "ok" {
		t.Errorf("resp.Body = %q, want %q", capturedExchange.Response.Body, "ok")
	}
	if capturedExchange.Response.Duration <= 0 {
		t.Error("resp.Duration should be positive")
	}
}

func TestHeadersForwardedToUpstream(t *testing.T) {
	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gw, err := New(Config{UpstreamURL: upstream.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go gw.Start()
	defer gw.Stop(context.Background())

	req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/test", gw.Addr()), nil)
	req.Header.Set("Authorization", "Bearer sk-test-123")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if receivedAuth != "Bearer sk-test-123" {
		t.Errorf("upstream Authorization = %q, want %q", receivedAuth, "Bearer sk-test-123")
	}
}

func TestQueryParamsForwarded(t *testing.T) {
	var receivedQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	gw, err := New(Config{UpstreamURL: upstream.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go gw.Start()
	defer gw.Stop(context.Background())

	resp, err := http.Get(fmt.Sprintf("http://%s/v1/models?limit=10&offset=0", gw.Addr()))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if receivedQuery != "limit=10&offset=0" {
		t.Errorf("upstream query = %q, want %q", receivedQuery, "limit=10&offset=0")
	}
}

func TestUpstreamUnavailable(t *testing.T) {
	gw, err := New(Config{UpstreamURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go gw.Start()
	defer gw.Stop(context.Background())

	resp, err := http.Get(fmt.Sprintf("http://%s/test", gw.Addr()))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
}

func TestRequiresUpstreamURL(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("expected error for empty UpstreamURL")
	}
}

func TestLoopbackBinding(t *testing.T) {
	gw, err := New(Config{UpstreamURL: "http://example.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Stop(context.Background())

	if !strings.HasPrefix(gw.Addr(), "127.0.0.1:") {
		t.Errorf("addr = %q, want 127.0.0.1:* prefix", gw.Addr())
	}
}

func TestPortAccessor(t *testing.T) {
	gw, err := New(Config{UpstreamURL: "http://example.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer gw.Stop(context.Background())

	if gw.Port() <= 0 {
		t.Errorf("port = %d, want > 0", gw.Port())
	}
}

func TestGenerateIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := GenerateID()
		if !strings.HasPrefix(id, "gw-") {
			t.Fatalf("id = %q, want gw- prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id: %s", id)
		}
		seen[id] = true
	}
}
