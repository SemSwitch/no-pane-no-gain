package claude

import (
	"encoding/json"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/gateway"
)

type requestEnvelope struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens,omitempty"`
	Messages  []requestMessage    `json:"messages"`
	Tools     []requestToolSchema `json:"tools"`
	Stream    bool                `json:"stream,omitempty"`
}

type requestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type requestToolSchema struct {
	Name string `json:"name"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type responseEnvelope struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Content    []contentBlock `json:"content"`
	Error      *apiError      `json:"error,omitempty"`
}

type apiError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

func normalizeRequest(meta adapters.SessionMeta, capture *gateway.RequestCapture) []coreEvent {
	if capture == nil {
		return nil
	}

	events := []coreEvent{
		newEvent(core.EventLLMRequest, meta, capture.RequestID, capture.ReceivedAt, core.ContentFull, rawOrEmpty(capture.Body), map[string]any{
			"method": capture.Method,
			"path":   capture.Path,
		}),
		newEvent(core.EventAgentBusy, meta, capture.RequestID, capture.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
			"reason": "request_sent",
			"path":   capture.Path,
		}), nil),
	}

	var envelope requestEnvelope
	if err := json.Unmarshal(capture.Body, &envelope); err != nil {
		return events
	}

	if events[0].Event.Metadata == nil {
		events[0].Event.Metadata = map[string]any{}
	}
	events[0].Event.Metadata["model"] = envelope.Model
	events[0].Event.Metadata["tool_count"] = len(envelope.Tools)
	events[0].Event.Metadata["message_count"] = len(envelope.Messages)

	for _, message := range envelope.Messages {
		for _, block := range decodeContentBlocks(message.Content) {
			if block.Type != "tool_result" {
				continue
			}

			events = append(events, newEvent(core.EventToolResult, meta, capture.RequestID, capture.ReceivedAt, core.ContentFull, mapPayload(map[string]any{
				"tool_use_id": block.ToolUseID,
				"content":     decodeFlexibleJSON(block.Content),
				"is_error":    block.IsError,
				"role":        message.Role,
			}), map[string]any{
				"tool_use_id": block.ToolUseID,
			}))
		}
	}

	return events
}

func normalizeResponse(meta adapters.SessionMeta, exchange *gateway.Exchange) []coreEvent {
	if exchange == nil || exchange.Response == nil {
		return nil
	}

	decodedBody, decodeErr := decodeResponseBody(exchange.Response.Header, exchange.Response.Body)
	metadata := map[string]any{
		"status_code": exchange.Response.StatusCode,
		"duration_ms": exchange.Response.Duration.Milliseconds(),
	}
	if contentEncoding := exchange.Response.Header.Get("Content-Encoding"); contentEncoding != "" {
		metadata["content_encoding"] = contentEncoding
	}
	if decodeErr != nil {
		metadata["decode_error"] = decodeErr.Error()
	}

	if decodeErr != nil {
		events := []coreEvent{
			newEvent(core.EventLLMResponse, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
				"status_code": exchange.Response.StatusCode,
				"duration_ms": exchange.Response.Duration.Milliseconds(),
				"message":     decodeErr.Error(),
				"type":        "decode_error",
			}), metadata),
		}
		events = append(events, newEvent(core.EventAgentError, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
			"status_code": exchange.Response.StatusCode,
			"message":     decodeErr.Error(),
			"type":        "decode_error",
		}), nil))
		return events
	}

	payloadBody := decodedBody
	var envelope responseEnvelope
	if looksLikeSSE(decodedBody) {
		streamPayload, streamEnvelope, err := parseStreamingResponse(decodedBody)
		if err != nil {
			events := []coreEvent{
				newEvent(core.EventLLMResponse, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
					"status_code": exchange.Response.StatusCode,
					"duration_ms": exchange.Response.Duration.Milliseconds(),
					"message":     err.Error(),
					"type":        "stream_decode_error",
				}), metadata),
			}
			events = append(events, newEvent(core.EventAgentError, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
				"status_code": exchange.Response.StatusCode,
				"message":     err.Error(),
				"type":        "stream_decode_error",
			}), nil))
			return events
		}
		payloadBody = streamPayload
		envelope = streamEnvelope
	} else if err := json.Unmarshal(decodedBody, &envelope); err != nil {
		events := []coreEvent{
			newEvent(core.EventLLMResponse, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
				"status_code": exchange.Response.StatusCode,
				"duration_ms": exchange.Response.Duration.Milliseconds(),
			}), metadata),
		}
		if exchange.Response.StatusCode >= 400 {
			events = append(events, newEvent(core.EventAgentError, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
				"status_code": exchange.Response.StatusCode,
			}), nil))
		}
		return events
	}

	events := []coreEvent{
		newEvent(core.EventLLMResponse, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentFull, rawOrEmpty(payloadBody), metadata),
	}

	if events[0].Event.Metadata == nil {
		events[0].Event.Metadata = map[string]any{}
	}
	events[0].Event.Metadata["message_id"] = envelope.ID
	events[0].Event.Metadata["model"] = envelope.Model
	events[0].Event.Metadata["stop_reason"] = envelope.StopReason

	if envelope.Type == "error" || envelope.Error != nil || exchange.Response.StatusCode >= 400 {
		events = append(events, newEvent(core.EventAgentError, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
			"status_code": exchange.Response.StatusCode,
			"type":        valueOr(envelope.Type, "error"),
			"message":     errorMessage(envelope.Error),
		}), nil))
		return events
	}

	hasToolUse := false
	for _, block := range envelope.Content {
		if block.Type != "tool_use" {
			continue
		}
		hasToolUse = true
		events = append(events, newEvent(core.EventToolCall, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentFull, mapPayload(map[string]any{
			"id":    block.ID,
			"name":  block.Name,
			"input": decodeFlexibleJSON(block.Input),
		}), map[string]any{
			"tool_name": block.Name,
		}))
	}

	switch {
	case hasToolUse:
		events = append(events, newEvent(core.EventAgentTooling, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
			"reason": "tool_use",
		}), nil))
	case envelope.StopReason == "end_turn":
		events = append(events, newEvent(core.EventAgentDone, meta, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, mapPayload(map[string]any{
			"reason": "end_turn",
		}), nil))
	}

	return events
}

type coreEvent struct {
	Event core.Event
}

func newEvent(kind core.EventKind, meta adapters.SessionMeta, requestID string, timestamp time.Time, mode core.ContentMode, payload json.RawMessage, metadata map[string]any) coreEvent {
	return coreEvent{
		Event: core.Event{
			Kind:        kind,
			Provider:    core.ProviderClaude,
			Role:        meta.Role,
			StableID:    meta.StableID,
			RepoRoot:    meta.RepoRoot,
			SessionID:   meta.SessionID,
			RequestID:   requestID,
			Timestamp:   timestamp.UTC(),
			ContentMode: mode,
			Source:      core.SourceGateway,
			Sensitivity: core.SensitivityLocalOnly,
			Payload:     payload,
			Metadata:    metadata,
		},
	}
}

func decodeContentBlocks(raw json.RawMessage) []contentBlock {
	if len(raw) == 0 {
		return nil
	}

	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return blocks
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		return []contentBlock{{Type: "text", Text: text}}
	}

	return nil
}

func decodeFlexibleJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}

	var value any
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
}

func rawOrEmpty(body []byte) json.RawMessage {
	if len(body) == 0 {
		return mapPayload(map[string]any{})
	}
	return json.RawMessage(body)
}

func mapPayload(payload map[string]any) json.RawMessage {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return body
}

func valueOr(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func errorMessage(err *apiError) string {
	if err == nil {
		return ""
	}
	return err.Message
}
