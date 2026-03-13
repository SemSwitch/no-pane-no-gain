package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func parseStreamingResponse(body []byte) ([]byte, responseEnvelope, error) {
	events, err := parseSSE(body)
	if err != nil {
		return nil, responseEnvelope{}, err
	}

	state := responseEnvelope{
		Type: "message",
		Role: "assistant",
	}
	blocks := map[int]*streamBlock{}
	order := map[int]struct{}{}

	for _, event := range events {
		switch stringField(event, "type") {
		case "message_start":
			message, _ := event["message"].(map[string]any)
			if message == nil {
				continue
			}
			state.ID = stringField(message, "id")
			state.Type = valueOr(stringField(message, "type"), state.Type)
			state.Role = valueOr(stringField(message, "role"), state.Role)
			state.Model = stringField(message, "model")
			if stopReason := stringField(message, "stop_reason"); stopReason != "" {
				state.StopReason = stopReason
			}
		case "content_block_start":
			index := intField(event, "index")
			content, _ := event["content_block"].(map[string]any)
			if content == nil {
				continue
			}
			block := getStreamBlock(blocks, index)
			order[index] = struct{}{}
			block.Type = valueOr(stringField(content, "type"), block.Type)
			if id := stringField(content, "id"); id != "" {
				block.ID = id
			}
			if name := stringField(content, "name"); name != "" {
				block.Name = name
			}
			if text := stringField(content, "text"); text != "" {
				block.Text.WriteString(text)
			}
			if input, ok := content["input"]; ok {
				raw, err := json.Marshal(input)
				if err == nil {
					block.Input.Reset()
					block.Input.Write(raw)
				}
			}
		case "content_block_delta":
			index := intField(event, "index")
			delta, _ := event["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			block := getStreamBlock(blocks, index)
			order[index] = struct{}{}
			switch stringField(delta, "type") {
			case "text_delta":
				block.Text.WriteString(stringField(delta, "text"))
			case "input_json_delta":
				block.Input.WriteString(stringField(delta, "partial_json"))
			}
		case "message_delta":
			delta, _ := event["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			if stopReason := stringField(delta, "stop_reason"); stopReason != "" {
				state.StopReason = stopReason
			}
		}
	}

	indexes := make([]int, 0, len(order))
	for index := range order {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	content := make([]contentBlock, 0, len(indexes))
	for _, index := range indexes {
		block := blocks[index]
		if block == nil {
			continue
		}
		switch block.Type {
		case "text":
			content = append(content, contentBlock{
				Type: "text",
				Text: block.Text.String(),
			})
		case "tool_use":
			content = append(content, contentBlock{
				Type:  "tool_use",
				ID:    block.ID,
				Name:  block.Name,
				Input: finalizeInputJSON([]byte(block.Input.String())),
			})
		}
	}

	state.Content = content
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, responseEnvelope{}, fmt.Errorf("marshal claude streaming response: %w", err)
	}
	return payload, state, nil
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
		if raw == "" || raw == "[DONE]" {
			return nil
		}

		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			return fmt.Errorf("decode claude sse event: %w", err)
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
		return nil, fmt.Errorf("scan claude sse stream: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}

	return events, nil
}

func looksLikeSSE(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(trimmed, "data:") || strings.HasPrefix(trimmed, "event:")
}

type streamBlock struct {
	Type  string
	ID    string
	Name  string
	Text  strings.Builder
	Input strings.Builder
}

func getStreamBlock(blocks map[int]*streamBlock, index int) *streamBlock {
	block := blocks[index]
	if block != nil {
		return block
	}
	block = &streamBlock{}
	blocks[index] = block
	return block
}

func finalizeInputJSON(raw []byte) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}

	var value any
	if err := json.Unmarshal(trimmed, &value); err == nil {
		return json.RawMessage(append([]byte(nil), trimmed...))
	}

	fallback, err := json.Marshal(string(trimmed))
	if err != nil {
		return nil
	}
	return json.RawMessage(fallback)
}

func stringField(source map[string]any, key string) string {
	value, _ := source[key].(string)
	return value
}

func intField(source map[string]any, key string) int {
	value, ok := source[key]
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}
