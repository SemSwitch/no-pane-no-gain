package gemini

import (
	"encoding/json"
	"fmt"
	"bytes"
	"strconv"
	"time"
)

type Payload struct {
	ResourceLogs  []ResourceLogs  `json:"resourceLogs,omitempty"`
	ResourceSpans []ResourceSpans `json:"resourceSpans,omitempty"`
}

type ResourceLogs struct {
	Resource  Resource    `json:"resource,omitempty"`
	ScopeLogs []ScopeLogs `json:"scopeLogs,omitempty"`
}

type ResourceSpans struct {
	Resource   Resource     `json:"resource,omitempty"`
	ScopeSpans []ScopeSpans `json:"scopeSpans,omitempty"`
}

type Resource struct {
	Attributes []KeyValue `json:"attributes,omitempty"`
}

type ScopeLogs struct {
	LogRecords []LogRecord `json:"logRecords,omitempty"`
}

type ScopeSpans struct {
	Spans []Span `json:"spans,omitempty"`
}

type LogRecord struct {
	TimeUnixNano string     `json:"timeUnixNano,omitempty"`
	Body         AnyValue   `json:"body,omitempty"`
	Attributes   []KeyValue `json:"attributes,omitempty"`
}

type Span struct {
	Name              string     `json:"name,omitempty"`
	StartTimeUnixNano string     `json:"startTimeUnixNano,omitempty"`
	EndTimeUnixNano   string     `json:"endTimeUnixNano,omitempty"`
	Attributes        []KeyValue `json:"attributes,omitempty"`
}

type KeyValue struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

type AnyValue struct {
	StringValue string       `json:"stringValue,omitempty"`
	BoolValue   bool         `json:"boolValue,omitempty"`
	IntValue    flexibleInt  `json:"intValue,omitempty"`
	DoubleValue float64      `json:"doubleValue,omitempty"`
	ArrayValue  *ArrayValue  `json:"arrayValue,omitempty"`
	KvlistValue *KVListValue `json:"kvlistValue,omitempty"`
}

type flexibleInt string

func (v *flexibleInt) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*v = ""
		return nil
	}

	var raw string
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		*v = flexibleInt(raw)
		return nil
	}

	*v = flexibleInt(string(trimmed))
	return nil
}

type ArrayValue struct {
	Values []AnyValue `json:"values,omitempty"`
}

type KVListValue struct {
	Values []KeyValue `json:"values,omitempty"`
}

func DecodePayload(body []byte) (Payload, error) {
	var payload Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Payload{}, fmt.Errorf("decode gemini otlp payload: %w", err)
	}
	return payload, nil
}

func (v AnyValue) Interface() any {
	switch {
	case v.StringValue != "":
		return v.StringValue
	case v.IntValue != "":
		if parsed, err := strconv.ParseInt(string(v.IntValue), 10, 64); err == nil {
			return parsed
		}
		return string(v.IntValue)
	case v.ArrayValue != nil:
		out := make([]any, 0, len(v.ArrayValue.Values))
		for _, item := range v.ArrayValue.Values {
			out = append(out, item.Interface())
		}
		return out
	case v.KvlistValue != nil:
		out := make(map[string]any, len(v.KvlistValue.Values))
		for _, item := range v.KvlistValue.Values {
			out[item.Key] = item.Value.Interface()
		}
		return out
	case v.DoubleValue != 0:
		return v.DoubleValue
	case v.BoolValue:
		return true
	default:
		return nil
	}
}

func AttributesToMap(groups ...[]KeyValue) map[string]any {
	out := map[string]any{}
	for _, group := range groups {
		for _, item := range group {
			out[item.Key] = item.Value.Interface()
		}
	}
	return out
}

func parseUnixNano(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, nanos).UTC(), true
}
