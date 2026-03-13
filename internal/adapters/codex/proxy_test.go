package codex

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestProxyForwardsModelsRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.4"}]}`))
	}))
	defer upstream.Close()

	proxy, err := newProxy(proxyConfig{
		ListenPort:       0,
		HTTPBaseURL:      upstream.URL,
		WSBaseURL:        strings.Replace(upstream.URL, "http://", "ws://", 1),
		HTTPResponsesURL: upstream.URL + "/backend-api/codex/responses",
		WSResponsesURL:   strings.Replace(upstream.URL, "http://", "ws://", 1) + "/backend-api/codex/responses",
	})
	if err != nil {
		t.Fatalf("newProxy() error = %v", err)
	}
	go proxy.Start()
	defer proxy.Stop(context.Background())

	resp, err := http.Get(fmt.Sprintf("http://%s/v1/models", proxy.Addr()))
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if string(body) != `{"data":[{"id":"gpt-5.4"}]}` {
		t.Fatalf("body = %q", body)
	}
}

func TestProxyRewritesResponsesHTTPFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("path = %q, want /backend-api/codex/responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"resp_123","output":[{"type":"message","content":[{"type":"output_text","text":"HTTP_OK"}]}]}`))
	}))
	defer upstream.Close()

	proxy, err := newProxy(proxyConfig{
		ListenPort:       0,
		HTTPBaseURL:      upstream.URL,
		WSBaseURL:        strings.Replace(upstream.URL, "http://", "ws://", 1),
		HTTPResponsesURL: upstream.URL + "/backend-api/codex/responses",
		WSResponsesURL:   strings.Replace(upstream.URL, "http://", "ws://", 1) + "/backend-api/codex/responses",
	})
	if err != nil {
		t.Fatalf("newProxy() error = %v", err)
	}
	go proxy.Start()
	defer proxy.Stop(context.Background())

	resp, err := http.Post(fmt.Sprintf("http://%s/v1/responses", proxy.Addr()), "application/json", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("POST /v1/responses: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if string(body) != `{"id":"resp_123","output":[{"type":"message","content":[{"type":"output_text","text":"HTTP_OK"}]}]}` {
		t.Fatalf("body = %q", body)
	}
}

func TestProxyBridgesResponsesWebSocket(t *testing.T) {
	var mu sync.Mutex
	var clientBodies []string
	var serverBodies []string

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Fatalf("path = %q, want /backend-api/codex/responses", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade upstream: %v", err)
		}
		defer conn.Close()

		mt, body, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("upstream read: %v", err)
		}
		if mt != websocket.TextMessage {
			t.Fatalf("upstream message type = %d, want text", mt)
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"WS_OK"}]}]}}`)); err != nil {
			t.Fatalf("upstream write: %v", err)
		}
		_ = string(body)
	}))
	defer upstream.Close()

	proxy, err := newProxy(proxyConfig{
		ListenPort:       0,
		HTTPBaseURL:      upstream.URL,
		WSBaseURL:        strings.Replace(upstream.URL, "http://", "ws://", 1),
		HTTPResponsesURL: upstream.URL + "/backend-api/codex/responses",
		WSResponsesURL:   strings.Replace(upstream.URL, "http://", "ws://", 1) + "/backend-api/codex/responses",
		OnWSClientMessage: func(_ context.Context, message *wsMessageCapture) {
			mu.Lock()
			defer mu.Unlock()
			clientBodies = append(clientBodies, string(message.Body))
		},
		OnWSServerMessage: func(_ context.Context, message *wsMessageCapture) {
			mu.Lock()
			defer mu.Unlock()
			serverBodies = append(serverBodies, string(message.Body))
		},
	})
	if err != nil {
		t.Fatalf("newProxy() error = %v", err)
	}
	go proxy.Start()
	defer proxy.Stop(context.Background())

	conn, _, err := websocket.DefaultDialer.Dial(fmt.Sprintf("ws://%s/v1/responses", proxy.Addr()), http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial proxy websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)); err != nil {
		t.Fatalf("write to proxy websocket: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, body, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read from proxy websocket: %v", err)
	}

	if string(body) != `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"WS_OK"}]}]}}` {
		t.Fatalf("body = %q", body)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(clientBodies) != 1 {
		t.Fatalf("clientBodies = %d, want 1", len(clientBodies))
	}
	if len(serverBodies) != 1 {
		t.Fatalf("serverBodies = %d, want 1", len(serverBodies))
	}
}
