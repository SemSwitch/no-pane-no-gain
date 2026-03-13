package codex

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

func TestPrepareLaunchSpecUsesLoopbackOpenAIBaseURL(t *testing.T) {
	factory := NewFactory()
	session, err := factory.Prepare(context.Background(), adapters.PrepareParams{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Meta: adapters.SessionMeta{
			Role:      "builder",
			Provider:  core.ProviderCodex,
			RepoRoot:  t.TempDir(),
			SessionID: "Builder Session/01",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer func() {
		if closeErr := session.Close(context.Background()); closeErr != nil {
			t.Fatalf("Close() error = %v", closeErr)
		}
	}()

	spec := session.LaunchSpec()
	if got := spec.Env["NO_PROXY"]; got != "127.0.0.1,localhost" {
		t.Fatalf("NO_PROXY = %q, want %q", got, "127.0.0.1,localhost")
	}
	if got := spec.Env["no_proxy"]; got != "127.0.0.1,localhost" {
		t.Fatalf("no_proxy = %q, want %q", got, "127.0.0.1,localhost")
	}
	if !strings.HasPrefix(spec.Env["OPENAI_BASE_URL"], "http://127.0.0.1:") || !strings.HasSuffix(spec.Env["OPENAI_BASE_URL"], "/v1") {
		t.Fatalf("OPENAI_BASE_URL = %q, want loopback /v1 URL", spec.Env["OPENAI_BASE_URL"])
	}
	if len(spec.ArgsPrefix) != 0 {
		t.Fatalf("ArgsPrefix = %#v, want empty", spec.ArgsPrefix)
	}
}

func TestRequestEventsEmitLLMRequestAndToolResult(t *testing.T) {
	meta := adapters.SessionMeta{
		Role:      "builder",
		StableID:  "st_builder",
		Provider:  core.ProviderCodex,
		RepoRoot:  "C:/repo",
		SessionID: "session-123",
	}

	body := mustReadFixture(t, "testdata/request.json")
	events, err := RequestEvents(meta, &httpRequestCapture{
		RequestID:  "gw-req-1",
		Method:     "POST",
		Path:       "/v1/responses",
		Body:       body,
		ReceivedAt: fixedTime(),
	})
	if err != nil {
		t.Fatalf("RequestEvents() error = %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	assertEvent(t, events[0], core.EventLLMRequest, core.ProviderCodex, "builder", "session-123", "gw-req-1")
	if events[0].StableID != "st_builder" {
		t.Fatalf("expected stable_id st_builder, got %q", events[0].StableID)
	}
	assertPayloadField(t, events[0], "http.path", "/v1/responses")

	assertEvent(t, events[1], core.EventToolResult, core.ProviderCodex, "builder", "session-123", "gw-req-1")
	assertPayloadField(t, events[1], "tool_result.call_id", "call_123")
}

func TestResponseEventsFromEventStreamEmitResponseToolCallAndDone(t *testing.T) {
	meta := adapters.SessionMeta{
		Role:      "builder",
		Provider:  core.ProviderCodex,
		RepoRoot:  "C:/repo",
		SessionID: "session-123",
	}

	body := mustReadFixture(t, "testdata/response.sse")
	events, err := ResponseEvents(meta, &httpExchange{
		RequestID: "gw-resp-1",
		Request: &httpRequestCapture{
			RequestID:  "gw-resp-1",
			Method:     "POST",
			Path:       "/v1/responses",
			ReceivedAt: fixedTime(),
		},
		Response: &httpResponseCapture{
			StatusCode: 200,
			Header: map[string][]string{
				"Content-Type": {"text/event-stream"},
			},
			Body:       body,
			Duration:   250000000,
			ReceivedAt: fixedTime(),
		},
	})
	if err != nil {
		t.Fatalf("ResponseEvents() error = %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	assertEvent(t, events[0], core.EventLLMResponse, core.ProviderCodex, "builder", "session-123", "gw-resp-1")
	assertPayloadField(t, events[0], "assistant_text", "All set.")

	assertEvent(t, events[1], core.EventToolCall, core.ProviderCodex, "builder", "session-123", "gw-resp-1")
	assertPayloadField(t, events[1], "tool_call.call_id", "call_123")
	assertPayloadField(t, events[1], "tool_call.name", "run_shell_command")

	assertEvent(t, events[2], core.EventAgentDone, core.ProviderCodex, "builder", "session-123", "gw-resp-1")
	assertPayloadField(t, events[2], "reason", "response.completed")
}

func TestRequestMessageEventsEmitLLMRequestAndToolResult(t *testing.T) {
	meta := adapters.SessionMeta{
		Role:      "builder",
		Provider:  core.ProviderCodex,
		RepoRoot:  "C:/repo",
		SessionID: "session-123",
	}

	events, err := RequestMessageEvents(meta, &wsMessageCapture{
		RequestID:   "gw-ws-req-1",
		Path:        "/v1/responses",
		MessageType: websocket.TextMessage,
		Body:        []byte(`{"type":"function_call_output","call_id":"call_123","output":"done"}`),
		ReceivedAt:  fixedTime(),
	})
	if err != nil {
		t.Fatalf("RequestMessageEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	assertEvent(t, events[0], core.EventLLMRequest, core.ProviderCodex, "builder", "session-123", "gw-ws-req-1")
	assertPayloadField(t, events[0], "transport", "websocket")

	assertEvent(t, events[1], core.EventToolResult, core.ProviderCodex, "builder", "session-123", "gw-ws-req-1")
	assertPayloadField(t, events[1], "tool_result.call_id", "call_123")
}

func TestResponseMessageEventsEmitToolCallCompletedResponseAndDone(t *testing.T) {
	meta := adapters.SessionMeta{
		Role:      "builder",
		StableID:  "st_builder",
		Provider:  core.ProviderCodex,
		RepoRoot:  "C:/repo",
		SessionID: "session-123",
	}
	state := &responseStreamState{}

	events, err := ResponseMessageEvents(meta, state, &wsMessageCapture{
		RequestID:   "gw-ws-resp-1",
		Path:        "/v1/responses",
		MessageType: websocket.TextMessage,
		Body:        []byte(`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_123","name":"run_shell_command"}}`),
		ReceivedAt:  fixedTime(),
	})
	if err != nil {
		t.Fatalf("ResponseMessageEvents(tool call) error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	assertEvent(t, events[0], core.EventLLMResponse, core.ProviderCodex, "builder", "session-123", "gw-ws-resp-1")
	assertEvent(t, events[1], core.EventToolCall, core.ProviderCodex, "builder", "session-123", "gw-ws-resp-1")
	assertPayloadField(t, events[1], "tool_call.call_id", "call_123")

	deltaEvents, err := ResponseMessageEvents(meta, state, &wsMessageCapture{
		RequestID:   "gw-ws-resp-1",
		Path:        "/v1/responses",
		MessageType: websocket.TextMessage,
		Body:        []byte(`{"type":"response.output_text.delta","delta":"INLINE_OK"}`),
		ReceivedAt:  fixedTime(),
	})
	if err != nil {
		t.Fatalf("ResponseMessageEvents(delta) error = %v", err)
	}
	if len(deltaEvents) != 1 {
		t.Fatalf("expected 1 event for delta frame, got %d", len(deltaEvents))
	}
	assertEvent(t, deltaEvents[0], core.EventLLMResponse, core.ProviderCodex, "builder", "session-123", "gw-ws-resp-1")
	assertPayloadField(t, deltaEvents[0], "assistant_text", "INLINE_OK")

	completedEvents, err := ResponseMessageEvents(meta, state, &wsMessageCapture{
		RequestID:   "gw-ws-resp-1",
		Path:        "/v1/responses",
		MessageType: websocket.TextMessage,
		Body:        []byte(`{"type":"response.completed","response":{"output":[]}}`),
		ReceivedAt:  fixedTime(),
	})
	if err != nil {
		t.Fatalf("ResponseMessageEvents(completed) error = %v", err)
	}
	if len(completedEvents) != 2 {
		t.Fatalf("expected 2 completed events, got %d", len(completedEvents))
	}
	assertEvent(t, completedEvents[0], core.EventLLMResponse, core.ProviderCodex, "builder", "session-123", "gw-ws-resp-1")
	assertPayloadField(t, completedEvents[0], "assistant_text", "INLINE_OK")
	assertEvent(t, completedEvents[1], core.EventAgentDone, core.ProviderCodex, "builder", "session-123", "gw-ws-resp-1")
	assertPayloadField(t, completedEvents[1], "reason", "response.completed")
	if !state.Completed() {
		t.Fatal("response stream state should be marked completed after response.completed")
	}
}

func TestWSErrorEventPreservesCaptureIdentity(t *testing.T) {
	meta := adapters.SessionMeta{
		Role:      "builder",
		StableID:  "st_builder",
		Provider:  core.ProviderCodex,
		RepoRoot:  "C:/repo",
		SessionID: "session-123",
	}

	event, err := wsErrorEvent(meta, &wsErrorCapture{
		RequestID:  "gw-ws-resp-1",
		Path:       "/v1/responses",
		Err:        context.DeadlineExceeded,
		ReceivedAt: fixedTime(),
	})
	if err != nil {
		t.Fatalf("wsErrorEvent() error = %v", err)
	}

	assertEvent(t, event, core.EventAgentError, core.ProviderCodex, "builder", "session-123", "gw-ws-resp-1")
	if event.StableID != "st_builder" {
		t.Fatalf("expected stable_id st_builder, got %q", event.StableID)
	}
	assertPayloadField(t, event, "error", context.DeadlineExceeded.Error())
	assertPayloadField(t, event, "http.path", "/v1/responses")
}

func mustReadFixture(t *testing.T, relative string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(relative))
	if err != nil {
		t.Fatalf("read fixture %s: %v", relative, err)
	}
	return body
}

func fixedTime() (ts time.Time) {
	return time.Unix(1700000000, 0).UTC()
}

func assertEvent(t *testing.T, event core.Event, kind core.EventKind, provider core.Provider, role string, sessionID string, requestID string) {
	t.Helper()
	if event.Kind != kind {
		t.Fatalf("expected kind %q, got %q", kind, event.Kind)
	}
	if event.Provider != provider {
		t.Fatalf("expected provider %q, got %q", provider, event.Provider)
	}
	if event.Role != role {
		t.Fatalf("expected role %q, got %q", role, event.Role)
	}
	if event.SessionID != sessionID {
		t.Fatalf("expected session %q, got %q", sessionID, event.SessionID)
	}
	if event.RequestID != requestID {
		t.Fatalf("expected request %q, got %q", requestID, event.RequestID)
	}
}

func assertPayloadField(t *testing.T, event core.Event, key string, want string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	got := lookup(payload, strings.Split(key, "."))
	if got != want {
		t.Fatalf("expected payload[%q] = %q, got %q", key, want, got)
	}
}

func lookup(value any, path []string) string {
	if len(path) == 0 {
		switch typed := value.(type) {
		case string:
			return typed
		default:
			return ""
		}
	}

	current, _ := value.(map[string]any)
	if current == nil {
		return ""
	}

	next, ok := current[path[0]]
	if !ok {
		return ""
	}
	return lookup(next, path[1:])
}
