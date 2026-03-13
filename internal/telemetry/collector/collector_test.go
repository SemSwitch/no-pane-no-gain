package collector

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestStart_HandlesLoopbackRequests(t *testing.T) {
	requests := make(chan Request, 1)

	server, err := Start(Options{
		Handler: func(ctx context.Context, request Request) error {
			requests <- request
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		_ = server.Close(context.Background())
	}()

	response, err := http.Post(server.Endpoint()+"/v1/logs", "application/json", bytes.NewReader([]byte(`{"ok":true}`)))
	if err != nil {
		t.Fatalf("POST collector: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected 200 response, got %d: %s", response.StatusCode, string(body))
	}

	select {
	case request := <-requests:
		if request.Method != http.MethodPost {
			t.Fatalf("expected method POST, got %s", request.Method)
		}
		if request.Path != "/v1/logs" {
			t.Fatalf("expected path /v1/logs, got %s", request.Path)
		}
		if request.Headers.Get("Content-Type") != "application/json" {
			t.Fatalf("expected content type application/json, got %s", request.Headers.Get("Content-Type"))
		}
		if got := string(request.Body); got != `{"ok":true}` {
			t.Fatalf("expected body %q, got %q", `{"ok":true}`, got)
		}
		if request.ReceivedAt.IsZero() {
			t.Fatal("expected non-zero ReceivedAt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for collector callback")
	}
}

func TestStart_RejectsUnsupportedMethod(t *testing.T) {
	server, err := Start(Options{
		Handler: func(context.Context, Request) error {
			t.Fatal("handler should not be called for GET requests")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		_ = server.Close(context.Background())
	}()

	response, err := http.Get(server.Endpoint() + "/v1/logs")
	if err != nil {
		t.Fatalf("GET collector: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 response, got %d", response.StatusCode)
	}
}

func TestStart_CloseStopsListener(t *testing.T) {
	server, err := Start(Options{
		Handler: func(context.Context, Request) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err = client.Post(server.Endpoint()+"/v1/logs", "application/json", bytes.NewReader([]byte(`{}`)))
	if err == nil {
		t.Fatal("expected request to fail after Close")
	}
}
