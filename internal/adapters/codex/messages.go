package codex

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

type responseStreamState struct {
	mu        sync.Mutex
	text      strings.Builder
	completed bool
}

func RequestMessageEvents(meta adapters.SessionMeta, message *wsMessageCapture) ([]core.Event, error) {
	if message == nil {
		return nil, fmt.Errorf("codex websocket request capture is nil")
	}

	body, err := decodePayload(nil, message.Body)
	if err != nil {
		body = rawBodyFallback(message.Body)
	}

	events := []core.Event{
		newMessageEvent(meta, core.EventLLMRequest, message.RequestID, message.ReceivedAt, map[string]any{
			"transport": "websocket",
			"path":      message.Path,
			"body":      body,
		}, map[string]any{
			"transport":    "websocket",
			"path":         message.Path,
			"message_type": wsMessageTypeName(message.MessageType),
		}),
	}

	for _, item := range extractToolResults(body) {
		events = append(events, newMessageEvent(meta, core.EventToolResult, message.RequestID, message.ReceivedAt, map[string]any{
			"tool_result": item,
		}, map[string]any{
			"transport": "websocket",
			"path":      message.Path,
		}))
	}

	return events, nil
}

func ResponseMessageEvents(meta adapters.SessionMeta, state *responseStreamState, message *wsMessageCapture) ([]core.Event, error) {
	if message == nil {
		return nil, fmt.Errorf("codex websocket response capture is nil")
	}

	body, err := decodePayload(nil, message.Body)
	if err != nil {
		body = rawBodyFallback(message.Body)
	}

	assistantText, toolCalls, completed := summarizeResponseFrame(state, body)
	payload := map[string]any{
		"transport": "websocket",
		"path":      message.Path,
		"body":      body,
	}
	if assistantText != "" {
		payload["assistant_text"] = assistantText
	}

	events := []core.Event{
		newMessageEvent(meta, core.EventLLMResponse, message.RequestID, message.ReceivedAt, payload, map[string]any{
			"transport":      "websocket",
			"path":           message.Path,
			"message_type":   wsMessageTypeName(message.MessageType),
			"assistant_text": assistantText,
		}),
	}

	for _, item := range toolCalls {
		events = append(events, newMessageEvent(meta, core.EventToolCall, message.RequestID, message.ReceivedAt, map[string]any{
			"tool_call": item,
		}, map[string]any{
			"transport": "websocket",
			"path":      message.Path,
		}))
	}

	if completed {
		events = append(events, newMessageEventWithMode(meta, core.EventAgentDone, message.RequestID, message.ReceivedAt, core.ContentMetadataOnly, map[string]any{
			"reason": "response.completed",
		}, map[string]any{
			"transport": "websocket",
			"path":      message.Path,
		}))
	}

	return events, nil
}

func newMessageEvent(meta adapters.SessionMeta, kind core.EventKind, requestID string, timestamp time.Time, payload any, metadata map[string]any) core.Event {
	return newMessageEventWithMode(meta, kind, requestID, timestamp, core.ContentFull, payload, metadata)
}

func newMessageEventWithMode(meta adapters.SessionMeta, kind core.EventKind, requestID string, timestamp time.Time, mode core.ContentMode, payload any, metadata map[string]any) core.Event {
	event := core.Event{
		Kind:        kind,
		Provider:    core.ProviderCodex,
		Role:        meta.Role,
		StableID:    meta.StableID,
		RepoRoot:    meta.RepoRoot,
		SessionID:   meta.SessionID,
		RequestID:   requestID,
		Timestamp:   timestamp.UTC(),
		ContentMode: mode,
		Source:      core.SourceGateway,
		Sensitivity: core.SensitivityLocalOnly,
		Metadata:    metadata,
	}
	if payload == nil {
		return event
	}
	body, err := marshalPayload(payload)
	if err == nil {
		event.Payload = body
	}
	return event
}

func summarizeResponseFrame(state *responseStreamState, body any) (string, []map[string]any, bool) {
	root, _ := body.(map[string]any)
	if root == nil {
		return "", nil, false
	}

	switch stringField(root, "type") {
	case "response.output_text.delta":
		delta := stringField(root, "delta")
		if delta != "" && state != nil {
			state.mu.Lock()
			state.text.WriteString(delta)
			state.mu.Unlock()
		}
		return delta, nil, false
	case "response.output_text.done":
		text := stringField(root, "text")
		if state != nil {
			state.mu.Lock()
			defer state.mu.Unlock()
			if text == "" {
				text = state.text.String()
			} else if state.text.Len() == 0 {
				state.text.WriteString(text)
			}
			return strings.TrimSpace(state.text.String()), nil, false
		}
		return text, nil, false
	case "response.output_item.added", "response.output_item.done":
		item, _ := root["item"].(map[string]any)
		if item == nil {
			return "", nil, false
		}
		text, toolCalls := summarizeDecodedResponse(map[string]any{"output": []any{item}})
		if text != "" && state != nil {
			state.mu.Lock()
			if state.text.Len() == 0 {
				state.text.WriteString(text)
			}
			state.mu.Unlock()
		}
		return text, toolCalls, false
	case "response.completed":
		response := root["response"]
		text, toolCalls := summarizeDecodedResponse(response)
		if state != nil {
			state.mu.Lock()
			defer state.mu.Unlock()
			state.completed = true
			if text == "" {
				text = state.text.String()
			} else if state.text.Len() == 0 {
				state.text.WriteString(text)
				text = state.text.String()
			}
			return strings.TrimSpace(text), toolCalls, true
		}
		return text, toolCalls, true
	default:
		text, toolCalls := summarizeDecodedResponse(root)
		return text, toolCalls, false
	}
}

func (s *responseStreamState) Completed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completed
}

func decodePayload(header http.Header, body []byte) (any, error) {
	decoded, err := decodeCompressedBody(header, body)
	if err != nil {
		return nil, err
	}
	return decodeJSONBody(decoded)
}

func decodeCompressedBody(header http.Header, body []byte) ([]byte, error) {
	encoding := ""
	if header != nil {
		encoding = strings.ToLower(strings.TrimSpace(header.Get("Content-Encoding")))
	}
	switch encoding {
	case "", "identity":
		return body, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	case "deflate":
		reader, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	default:
		return body, nil
	}
}

func rawBodyFallback(body []byte) map[string]any {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return map[string]any{"raw": ""}
	}
	if utf8.Valid(trimmed) {
		return map[string]any{"raw": string(trimmed)}
	}
	return map[string]any{"raw_base64": base64.StdEncoding.EncodeToString(trimmed)}
}

func marshalPayload(payload any) ([]byte, error) {
	return json.Marshal(payload)
}

func wsMessageTypeName(messageType int) string {
	switch messageType {
	case websocket.TextMessage:
		return "text"
	case websocket.BinaryMessage:
		return "binary"
	case websocket.CloseMessage:
		return "close"
	case websocket.PingMessage:
		return "ping"
	case websocket.PongMessage:
		return "pong"
	default:
		return fmt.Sprintf("type_%d", messageType)
	}
}
