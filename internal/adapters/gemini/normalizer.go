package gemini

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

type Context struct {
	RoleOverride string
	RepoRoot     string
	Now          func() time.Time
}

func (c Context) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func NormalizePayload(payload Payload, ctx Context) ([]core.Event, error) {
	var events []core.Event

	for _, batch := range payload.ResourceLogs {
		resourceAttrs := AttributesToMap(batch.Resource.Attributes)
		for _, scope := range batch.ScopeLogs {
			for _, record := range scope.LogRecords {
				normalized, err := normalizeLogRecord(resourceAttrs, record, ctx)
				if err != nil {
					return nil, err
				}
				events = append(events, normalized...)
			}
		}
	}

	for _, batch := range payload.ResourceSpans {
		resourceAttrs := AttributesToMap(batch.Resource.Attributes)
		for _, scope := range batch.ScopeSpans {
			for _, span := range scope.Spans {
				normalized, err := normalizeSpan(resourceAttrs, span, ctx)
				if err != nil {
					return nil, err
				}
				events = append(events, normalized...)
			}
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	return events, nil
}

func DecodeAndNormalize(body []byte, ctx Context) ([]core.Event, error) {
	payload, err := DecodePayload(body)
	if err != nil {
		return nil, err
	}
	return NormalizePayload(payload, ctx)
}

func normalizeLogRecord(resourceAttrs map[string]any, record LogRecord, ctx Context) ([]core.Event, error) {
	attrs := AttributesToMap(record.Attributes)
	merged := mergeMaps(resourceAttrs, attrs)
	role := roleFromAttributes(ctx, merged)
	sessionID := stringFromAny(merged["session.id"])
	timestamp := timestampFromLog(record, ctx)
	eventName := bodyName(record.Body)
	if eventName == "" {
		eventName = stringFromAny(merged["event.name"])
	}

	switch eventName {
	case "gemini_cli.user_prompt":
		prompt := stringFromAny(merged["prompt"])
		return []core.Event{
			newEvent(core.EventLLMRequest, role, sessionID, timestamp, core.ContentFull, core.SourceOTLP, core.SensitivityLocalOnly, map[string]any{
				"event_name": "gemini_cli.user_prompt",
				"prompt":     prompt,
			}),
			newEvent(core.EventAgentBusy, role, sessionID, timestamp, core.ContentMetadataOnly, core.SourceOTLP, core.SensitivityLocalOnly, map[string]any{
				"reason": "user_prompt",
			}),
		}, nil
	case "gemini_cli.tool_call":
		payload := map[string]any{
			"event_name":    "gemini_cli.tool_call",
			"function_name": stringFromAny(merged["function_name"]),
			"function_args": stringFromAny(merged["function_args"]),
			"decision":      stringFromAny(merged["decision"]),
			"success":       merged["success"],
			"tool_type":     stringFromAny(merged["tool_type"]),
		}
		return []core.Event{
			newEvent(core.EventToolCall, role, sessionID, timestamp, core.ContentFull, core.SourceOTLP, core.SensitivityLocalOnly, payload),
			newEvent(core.EventAgentTooling, role, sessionID, timestamp, core.ContentMetadataOnly, core.SourceOTLP, core.SensitivityLocalOnly, map[string]any{
				"reason":        "tool_call",
				"function_name": payload["function_name"],
			}),
		}, nil
	default:
		return nil, nil
	}
}

func normalizeSpan(resourceAttrs map[string]any, span Span, ctx Context) ([]core.Event, error) {
	attrs := AttributesToMap(span.Attributes)
	merged := mergeMaps(resourceAttrs, attrs)
	role := roleFromAttributes(ctx, merged)
	sessionID := stringFromAny(merged["session.id"])
	timestamp := timestampFromSpan(span, ctx)

	switch span.Name {
	case "tool_call":
		payload := map[string]any{
			"name":        stringFromAny(merged["request.name"]),
			"call_id":     stringFromAny(merged["request.callId"]),
			"command":     stringFromAny(merged["request.args.command"]),
			"description": stringFromAny(merged["request.args.description"]),
			"status":      stringFromAny(merged["status"]),
		}
		return []core.Event{
			newEvent(core.EventToolResult, role, sessionID, timestamp, core.ContentFull, core.SourceOTLP, core.SensitivityLocalOnly, payload),
		}, nil
	case "llm_call":
		outputJSON := stringFromAny(merged["gen_ai.output.messages"])
		if outputJSON == "" {
			return nil, nil
		}
		summary, err := summarizeOutputMessages(outputJSON)
		if err != nil {
			return nil, fmt.Errorf("summarize gemini llm_call output: %w", err)
		}

		if summary.HasToolCall {
			return []core.Event{
				newEvent(core.EventAgentTooling, role, sessionID, timestamp, core.ContentMetadataOnly, core.SourceOTLP, core.SensitivityLocalOnly, map[string]any{
					"reason": "llm_output_tool_call",
				}),
			}, nil
		}

		if !summary.HasVisibleText {
			return nil, nil
		}

		payload := map[string]any{
			"text": summary.Text,
		}
		return []core.Event{
			newEvent(core.EventLLMResponse, role, sessionID, timestamp, core.ContentFull, core.SourceOTLP, core.SensitivityLocalOnly, payload),
			newEvent(core.EventAgentDone, role, sessionID, timestamp, core.ContentMetadataOnly, core.SourceOTLP, core.SensitivityLocalOnly, map[string]any{
				"reason": "final_llm_call",
			}),
		}, nil
	default:
		return nil, nil
	}
}

func newEvent(kind core.EventKind, role string, sessionID string, timestamp time.Time, contentMode core.ContentMode, source core.SourceKind, sensitivity core.Sensitivity, payload map[string]any) core.Event {
	event := core.Event{
		Kind:        kind,
		Provider:    core.ProviderGemini,
		Role:        role,
		SessionID:   sessionID,
		Timestamp:   timestamp,
		ContentMode: contentMode,
		Source:      source,
		Sensitivity: sensitivity,
		Metadata:    map[string]any{},
	}

	if len(payload) != 0 {
		body, err := json.Marshal(payload)
		if err == nil {
			event.Payload = body
		}
	}

	return event
}

func roleFromAttributes(ctx Context, attrs map[string]any) string {
	if ctx.RoleOverride != "" {
		return ctx.RoleOverride
	}
	if role := stringFromAny(attrs["instance.role"]); role != "" {
		return role
	}
	return "gemini"
}

func timestampFromLog(record LogRecord, ctx Context) time.Time {
	if ts, ok := parseUnixNano(record.TimeUnixNano); ok {
		return ts
	}
	return ctx.now()
}

func timestampFromSpan(span Span, ctx Context) time.Time {
	if ts, ok := parseUnixNano(span.EndTimeUnixNano); ok {
		return ts
	}
	if ts, ok := parseUnixNano(span.StartTimeUnixNano); ok {
		return ts
	}
	return ctx.now()
}

func bodyName(value AnyValue) string {
	return strings.TrimSpace(value.StringValue)
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case int64:
		return fmt.Sprintf("%d", typed)
	case int:
		return fmt.Sprintf("%d", typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func mergeMaps(groups ...map[string]any) map[string]any {
	merged := map[string]any{}
	for _, group := range groups {
		for key, value := range group {
			merged[key] = value
		}
	}
	return merged
}

type outputSummary struct {
	Text           string
	HasVisibleText bool
	HasToolCall    bool
}

func summarizeOutputMessages(raw string) (outputSummary, error) {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return outputSummary{}, err
	}

	var visible []string
	hasToolCall := false

	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			if _, ok := typed["tool_call"]; ok {
				hasToolCall = true
			}
			if name, ok := typed["name"].(string); ok && name != "" && typed["arguments"] != nil {
				hasToolCall = true
			}

			if inlineData, ok := typed["inline_data"].(map[string]any); ok {
				if _, exists := inlineData["mime_type"]; exists {
					return
				}
			}

			if text, ok := typed["text"].(string); ok && strings.TrimSpace(text) != "" {
				if thought, ok := typed["thought"].(bool); !ok || !thought {
					visible = append(visible, text)
				}
			}

			if parts, ok := typed["parts"]; ok {
				walk(parts)
			}
			if messages, ok := typed["messages"]; ok {
				walk(messages)
			}
			if content, ok := typed["content"]; ok {
				walk(content)
			}
			if candidates, ok := typed["candidates"]; ok {
				walk(candidates)
			}
		}
	}
	walk(decoded)

	return outputSummary{
		Text:           strings.TrimSpace(strings.Join(visible, "")),
		HasVisibleText: len(visible) > 0,
		HasToolCall:    hasToolCall,
	}, nil
}
