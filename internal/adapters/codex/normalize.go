package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

func RequestEvents(meta adapters.SessionMeta, req *httpRequestCapture) ([]core.Event, error) {
	if req == nil {
		return nil, fmt.Errorf("codex request capture is nil")
	}

	body, err := decodePayload(req.Header, req.Body)
	if err != nil {
		body = rawBodyFallback(req.Body)
	}

	events := []core.Event{
		newEvent(meta, core.EventLLMRequest, req.RequestID, req.ReceivedAt, map[string]any{
			"http": map[string]any{
				"method": req.Method,
				"path":   req.Path,
			},
			"body": body,
		}, map[string]any{
			"http_method": req.Method,
			"http_path":   req.Path,
		}),
	}

	for _, item := range extractToolResults(body) {
		events = append(events, newEvent(meta, core.EventToolResult, req.RequestID, req.ReceivedAt, map[string]any{
			"tool_result": item,
		}, map[string]any{
			"http_path": req.Path,
		}))
	}

	return events, nil
}

func ResponseEvents(meta adapters.SessionMeta, exchange *httpExchange) ([]core.Event, error) {
	if exchange == nil || exchange.Request == nil || exchange.Response == nil {
		return nil, fmt.Errorf("codex response exchange is incomplete")
	}

	summary, err := summarizeResponse(exchange.Response)
	if err != nil {
		return nil, err
	}

	events := []core.Event{
		newEvent(meta, core.EventLLMResponse, exchange.RequestID, exchange.Response.ReceivedAt, map[string]any{
			"http": map[string]any{
				"status_code":  exchange.Response.StatusCode,
				"content_type": exchange.Response.Header.Get("Content-Type"),
				"path":         exchange.Request.Path,
				"duration_ms":  exchange.Response.Duration.Milliseconds(),
			},
			"assistant_text": summary.AssistantText,
			"body":           summary.Body,
		}, map[string]any{
			"http_status":    exchange.Response.StatusCode,
			"http_path":      exchange.Request.Path,
			"duration_ms":    exchange.Response.Duration.Milliseconds(),
			"content_type":   exchange.Response.Header.Get("Content-Type"),
			"assistant_text": summary.AssistantText,
		}),
	}

	for _, item := range summary.ToolCalls {
		events = append(events, newEvent(meta, core.EventToolCall, exchange.RequestID, exchange.Response.ReceivedAt, map[string]any{
			"tool_call": item,
		}, map[string]any{
			"http_path": exchange.Request.Path,
		}))
	}

	if summary.Completed {
		events = append(events, newEventWithMode(meta, core.EventAgentDone, exchange.RequestID, exchange.Response.ReceivedAt, core.ContentMetadataOnly, map[string]any{
			"reason": "response.completed",
		}, map[string]any{
			"http_path": exchange.Request.Path,
		}))
	}

	return events, nil
}

type responseSummary struct {
	Body          any
	AssistantText string
	ToolCalls     []map[string]any
	Completed     bool
}

func summarizeResponse(resp *httpResponseCapture) (responseSummary, error) {
	if resp == nil {
		return responseSummary{}, fmt.Errorf("codex response capture is nil")
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.Contains(contentType, "text/event-stream") || looksLikeSSE(resp.Body) {
		return summarizeEventStream(resp.Body)
	}

	body, err := decodePayload(resp.Header, resp.Body)
	if err != nil {
		return responseSummary{
			Body: rawBodyFallback(resp.Body),
		}, nil
	}

	assistantText, toolCalls := summarizeDecodedResponse(body)
	return responseSummary{
		Body:          body,
		AssistantText: assistantText,
		ToolCalls:     toolCalls,
		Completed:     responseCompleted(body),
	}, nil
}

func summarizeEventStream(body []byte) (responseSummary, error) {
	events, err := parseSSE(body)
	if err != nil {
		return responseSummary{}, err
	}

	var finalResponse any
	items := make([]map[string]any, 0, 4)
	var text strings.Builder
	completed := false

	for _, event := range events {
		eventType := stringField(event, "type")
		switch eventType {
		case "response.output_text.delta":
			text.WriteString(stringField(event, "delta"))
		case "response.output_text.done":
			text.WriteString(stringField(event, "text"))
		case "response.output_item.added", "response.output_item.done":
			if item, ok := event["item"].(map[string]any); ok {
				items = append(items, item)
			}
		case "response.completed":
			completed = true
			if response, ok := event["response"]; ok {
				finalResponse = response
			}
		}
	}

	if finalResponse == nil {
		finalResponse = map[string]any{
			"object": "response",
			"output": items,
		}
	}

	assistantText, toolCalls := summarizeDecodedResponse(finalResponse)
	if assistantText == "" && text.Len() != 0 {
		assistantText = strings.TrimSpace(text.String())
	}

	return responseSummary{
		Body:          finalResponse,
		AssistantText: assistantText,
		ToolCalls:     toolCalls,
		Completed:     completed || responseCompleted(finalResponse),
	}, nil
}

func parseSSE(body []byte) ([]map[string]any, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	events := make([]map[string]any, 0, 8)
	var dataLines []string

	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		raw := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if raw == "[DONE]" {
			return nil
		}

		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			return fmt.Errorf("decode codex sse event: %w", err)
		}
		events = append(events, decoded)
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan codex sse stream: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}

	return events, nil
}

func summarizeDecodedResponse(body any) (string, []map[string]any) {
	root, _ := body.(map[string]any)
	if root == nil {
		return "", nil
	}

	if output, ok := root["output"].([]any); ok {
		return summarizeResponseOutput(output)
	}

	if choices, ok := root["choices"].([]any); ok {
		return summarizeChatChoices(choices)
	}

	return "", nil
}

func summarizeResponseOutput(output []any) (string, []map[string]any) {
	var textParts []string
	toolCalls := make([]map[string]any, 0, 2)

	for _, entry := range output {
		item, _ := entry.(map[string]any)
		if item == nil {
			continue
		}

		switch stringField(item, "type") {
		case "function_call":
			toolCalls = append(toolCalls, item)
		case "message":
			textParts = append(textParts, extractMessageText(item)...)
		}
	}

	return strings.TrimSpace(strings.Join(textParts, "")), toolCalls
}

func summarizeChatChoices(choices []any) (string, []map[string]any) {
	var textParts []string
	toolCalls := make([]map[string]any, 0, 2)

	for _, entry := range choices {
		choice, _ := entry.(map[string]any)
		if choice == nil {
			continue
		}
		message, _ := choice["message"].(map[string]any)
		if message == nil {
			continue
		}

		if raw, ok := message["tool_calls"].([]any); ok {
			for _, item := range raw {
				call, _ := item.(map[string]any)
				if call != nil {
					toolCalls = append(toolCalls, call)
				}
			}
		}

		switch content := message["content"].(type) {
		case string:
			if strings.TrimSpace(content) != "" {
				textParts = append(textParts, content)
			}
		case []any:
			for _, item := range content {
				part, _ := item.(map[string]any)
				if part == nil {
					continue
				}
				if text := strings.TrimSpace(stringField(part, "text")); text != "" {
					textParts = append(textParts, text)
				}
			}
		}
	}

	return strings.TrimSpace(strings.Join(textParts, "")), toolCalls
}

func extractMessageText(message map[string]any) []string {
	content, _ := message["content"].([]any)
	if len(content) == 0 {
		return nil
	}

	parts := make([]string, 0, len(content))
	for _, entry := range content {
		item, _ := entry.(map[string]any)
		if item == nil {
			continue
		}

		switch stringField(item, "type") {
		case "output_text", "text", "input_text":
			if text := strings.TrimSpace(stringField(item, "text")); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return parts
}

func extractToolResults(body any) []map[string]any {
	results := make([]map[string]any, 0, 2)
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			itemType := stringField(typed, "type")
			switch {
			case itemType == "function_call_output":
				results = append(results, typed)
			case stringField(typed, "role") == "tool" && stringField(typed, "tool_call_id") != "":
				results = append(results, typed)
			}
			for _, nested := range typed {
				walk(nested)
			}
		}
	}

	walk(body)
	return results
}

func newEvent(meta adapters.SessionMeta, kind core.EventKind, requestID string, timestamp time.Time, payload any, metadata map[string]any) core.Event {
	return newEventWithMode(meta, kind, requestID, timestamp, core.ContentFull, payload, metadata)
}

func newEventWithMode(meta adapters.SessionMeta, kind core.EventKind, requestID string, timestamp time.Time, mode core.ContentMode, payload any, metadata map[string]any) core.Event {
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

	body, err := json.Marshal(payload)
	if err == nil {
		event.Payload = body
	}
	return event
}

func responseCompleted(body any) bool {
	root, _ := body.(map[string]any)
	if root == nil {
		return false
	}

	switch stringField(root, "type") {
	case "response.completed":
		return true
	}

	return strings.EqualFold(stringField(root, "status"), "completed")
}

func decodeJSONBody(body []byte) (any, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}

	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func looksLikeSSE(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "event:")
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprintf("%v", typed)
	}
}
