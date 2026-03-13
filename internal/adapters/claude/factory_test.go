package claude

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/gateway"
)

func TestPrepareSetsLoopbackLaunchEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example.test")
	t.Setenv("NO_PROXY", "corp.internal")
	t.Setenv("no_proxy", "team.internal")

	factory := NewFactory()
	session, err := factory.Prepare(context.Background(), adapters.PrepareParams{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Publisher: nil,
		Meta: adapters.SessionMeta{
			Role:      "builder",
			Provider:  core.ProviderClaude,
			RepoRoot:  `C:\Nexis`,
			SessionID: "sess-claude-1",
		},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := session.Close(closeCtx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	spec := session.LaunchSpec()
	baseURL := spec.Env["ANTHROPIC_BASE_URL"]
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want loopback gateway", baseURL)
	}
	assertNoProxyContains(t, spec.Env["NO_PROXY"], "127.0.0.1", "localhost")
	assertNoProxyContains(t, spec.Env["no_proxy"], "127.0.0.1", "localhost")
}

func TestNormalizeRequestPublishesRequestAndToolResult(t *testing.T) {
	body := readFixture(t, "request_tool_result.json")
	now := time.Date(2026, 3, 12, 11, 15, 0, 0, time.UTC)

	events := normalizeRequest(testMeta(), &gateway.RequestCapture{
		RequestID:  "gw-req-1",
		Method:     "POST",
		Path:       "/v1/messages",
		Body:       body,
		ReceivedAt: now,
	})

	requireKinds(t, events, core.EventLLMRequest, core.EventAgentBusy, core.EventToolResult)

	requestEvent := eventByKind(t, events, core.EventLLMRequest)
	if requestEvent.Metadata["model"] != "claude-sonnet-4-5" {
		t.Fatalf("request model metadata = %v", requestEvent.Metadata["model"])
	}
	if requestEvent.RequestID != "gw-req-1" {
		t.Fatalf("request RequestID = %q", requestEvent.RequestID)
	}
	requestPayload := decodePayloadMap(t, requestEvent.Payload)
	if requestPayload["model"] != "claude-sonnet-4-5" {
		t.Fatalf("request payload model = %v", requestPayload["model"])
	}

	toolResult := eventByKind(t, events, core.EventToolResult)
	toolPayload := decodePayloadMap(t, toolResult.Payload)
	if toolPayload["tool_use_id"] != "toolu_req_01" {
		t.Fatalf("tool_use_id = %v", toolPayload["tool_use_id"])
	}
	if toolPayload["content"] != "C:\\Nexis" {
		t.Fatalf("tool content = %v", toolPayload["content"])
	}
}

func TestNormalizeResponsePublishesToolCallAndDone(t *testing.T) {
	now := time.Date(2026, 3, 12, 11, 20, 0, 0, time.UTC)

	toolEvents := normalizeResponse(testMeta(), &gateway.Exchange{
		RequestID: "gw-resp-tool",
		Response: &gateway.ResponseCapture{
			StatusCode: 200,
			Body:       readFixture(t, "response_tool_use.json"),
			Duration:   150 * time.Millisecond,
			ReceivedAt: now,
		},
	})
	requireKinds(t, toolEvents, core.EventLLMResponse, core.EventToolCall, core.EventAgentTooling)
	toolCall := eventByKind(t, toolEvents, core.EventToolCall)
	toolPayload := decodePayloadMap(t, toolCall.Payload)
	if toolPayload["name"] != "edit_file" {
		t.Fatalf("tool name = %v", toolPayload["name"])
	}
	input := toolPayload["input"].(map[string]any)
	if input["path"] != "internal/app/main.go" {
		t.Fatalf("tool input path = %v", input["path"])
	}

	doneEvents := normalizeResponse(testMeta(), &gateway.Exchange{
		RequestID: "gw-resp-done",
		Response: &gateway.ResponseCapture{
			StatusCode: 200,
			Body:       readFixture(t, "response_done.json"),
			Duration:   80 * time.Millisecond,
			ReceivedAt: now.Add(time.Second),
		},
	})
	requireKinds(t, doneEvents, core.EventLLMResponse, core.EventAgentDone)
	done := eventByKind(t, doneEvents, core.EventAgentDone)
	donePayload := decodePayloadMap(t, done.Payload)
	if donePayload["reason"] != "end_turn" {
		t.Fatalf("done reason = %v", donePayload["reason"])
	}
}

func TestNormalizeResponseDecompressesGzipBody(t *testing.T) {
	assertCompressedResponseDone(t, "gzip", gzipBytes(t, readFixture(t, "response_done.json")))
}

func TestNormalizeResponseDecompressesDeflateBody(t *testing.T) {
	assertCompressedResponseDone(t, "deflate", zlibBytes(t, readFixture(t, "response_done.json")))
}

func TestNormalizeResponseDecompressesBrotliBody(t *testing.T) {
	assertCompressedResponseDone(t, "br", brotliBytes(t, readFixture(t, "response_done.json")))
}

func TestNormalizeResponseParsesCompressedStreamingBody(t *testing.T) {
	now := time.Date(2026, 3, 12, 11, 22, 0, 0, time.UTC)
	events := normalizeResponse(testMeta(), &gateway.Exchange{
		RequestID: "gw-resp-stream",
		Response: &gateway.ResponseCapture{
			StatusCode: 200,
			Header: http.Header{
				"Content-Encoding": []string{"gzip"},
				"Content-Type":     []string{"text/event-stream"},
			},
			Body:       gzipBytes(t, readFixture(t, "response_done.sse")),
			Duration:   95 * time.Millisecond,
			ReceivedAt: now,
		},
	})

	requireKinds(t, events, core.EventLLMResponse, core.EventAgentDone)

	response := eventByKind(t, events, core.EventLLMResponse)
	payload := decodePayloadMap(t, response.Payload)
	if payload["stop_reason"] != "end_turn" {
		t.Fatalf("stream payload stop_reason = %v", payload["stop_reason"])
	}
	content, ok := payload["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("stream payload content = %#v", payload["content"])
	}
	first, ok := content[0].(map[string]any)
	if !ok || first["text"] != "Streamed done." {
		t.Fatalf("stream text block = %#v", payload["content"])
	}
}

func testMeta() adapters.SessionMeta {
	return adapters.SessionMeta{
		Role:      "builder",
		Provider:  core.ProviderClaude,
		RepoRoot:  `C:\Nexis`,
		SessionID: "sess-claude-1",
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", name, err)
	}
	return body
}

func requireKinds(t *testing.T, events []coreEvent, kinds ...core.EventKind) {
	t.Helper()
	for _, kind := range kinds {
		_ = eventByKind(t, events, kind)
	}
}

func eventByKind(t *testing.T, events []coreEvent, kind core.EventKind) core.Event {
	t.Helper()
	for _, event := range events {
		if event.Event.Kind == kind {
			return event.Event
		}
	}
	t.Fatalf("missing event kind %s", kind)
	return core.Event{}
}

func decodePayloadMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("json.Unmarshal payload: %v", err)
	}
	return payload
}

func assertNoProxyContains(t *testing.T, value string, expected ...string) {
	t.Helper()
	parts := strings.Split(value, ",")
	set := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		set[trimmed] = struct{}{}
	}
	for _, item := range expected {
		if _, ok := set[item]; !ok {
			t.Fatalf("no_proxy missing %q in %q", item, value)
		}
	}
}

func assertCompressedResponseDone(t *testing.T, contentEncoding string, body []byte) {
	t.Helper()

	now := time.Date(2026, 3, 12, 11, 21, 0, 0, time.UTC)
	events := normalizeResponse(testMeta(), &gateway.Exchange{
		RequestID: "gw-resp-compressed",
		Response: &gateway.ResponseCapture{
			StatusCode: 200,
			Header:     httpHeader("Content-Encoding", contentEncoding),
			Body:       body,
			Duration:   80 * time.Millisecond,
			ReceivedAt: now,
		},
	})

	requireKinds(t, events, core.EventLLMResponse, core.EventAgentDone)

	response := eventByKind(t, events, core.EventLLMResponse)
	if response.Metadata["content_encoding"] != contentEncoding {
		t.Fatalf("content_encoding = %v", response.Metadata["content_encoding"])
	}
	payload := decodePayloadMap(t, response.Payload)
	if payload["stop_reason"] != "end_turn" {
		t.Fatalf("response payload stop_reason = %v", payload["stop_reason"])
	}

	done := eventByKind(t, events, core.EventAgentDone)
	donePayload := decodePayloadMap(t, done.Payload)
	if donePayload["reason"] != "end_turn" {
		t.Fatalf("done reason = %v", donePayload["reason"])
	}
}

func httpHeader(key string, value string) http.Header {
	return http.Header{key: []string{value}}
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func zlibBytes(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := zlib.NewWriter(&buf)
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

func brotliBytes(t *testing.T, body []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := brotli.NewWriter(&buf)
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("brotli write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("brotli close: %v", err)
	}
	return buf.Bytes()
}
