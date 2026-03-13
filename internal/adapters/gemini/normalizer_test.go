package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

func TestNormalizePayload_Logs(t *testing.T) {
	body := mustReadFixture(t, "testdata/logs.json")
	events, err := DecodeAndNormalize(body, Context{})
	if err != nil {
		t.Fatalf("DecodeAndNormalize() error = %v", err)
	}

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}

	assertEvent(t, events[0], core.EventLLMRequest, core.ProviderGemini, "Architect", "session-123")
	assertPayloadField(t, events[0], "prompt", "Reply with exactly: GEMINI_OTEL_TEST")

	assertEvent(t, events[1], core.EventAgentBusy, core.ProviderGemini, "Architect", "session-123")

	assertEvent(t, events[2], core.EventToolCall, core.ProviderGemini, "Architect", "session-123")
	assertPayloadField(t, events[2], "function_name", "run_shell_command")

	assertEvent(t, events[3], core.EventAgentTooling, core.ProviderGemini, "Architect", "session-123")
}

func TestNormalizePayload_Spans(t *testing.T) {
	body := mustReadFixture(t, "testdata/spans.json")
	events, err := DecodeAndNormalize(body, Context{
		Now: func() time.Time {
			return time.Unix(0, 0).UTC()
		},
	})
	if err != nil {
		t.Fatalf("DecodeAndNormalize() error = %v", err)
	}

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}

	assertEvent(t, events[0], core.EventAgentTooling, core.ProviderGemini, "Architect", "session-123")
	assertPayloadField(t, events[0], "reason", "llm_output_tool_call")

	assertEvent(t, events[1], core.EventToolResult, core.ProviderGemini, "Architect", "session-123")
	assertPayloadField(t, events[1], "name", "run_shell_command")

	assertEvent(t, events[2], core.EventLLMResponse, core.ProviderGemini, "Architect", "session-123")
	assertPayloadField(t, events[2], "text", "GEMINI_TOOL_DONE")

	assertEvent(t, events[3], core.EventAgentDone, core.ProviderGemini, "Architect", "session-123")
	assertPayloadField(t, events[3], "reason", "final_llm_call")
}

func TestDecodeAndNormalize_AcceptsNumericIntValue(t *testing.T) {
	body := []byte(`{
  "resourceLogs": [
    {
      "resource": {
        "attributes": [
          {
            "key": "session.id",
            "value": {
              "intValue": 123
            }
          },
          {
            "key": "instance.role",
            "value": {
              "stringValue": "Architect"
            }
          }
        ]
      },
      "scopeLogs": [
        {
          "logRecords": [
            {
              "timeUnixNano": "1000000000",
              "body": {
                "stringValue": "gemini_cli.user_prompt"
              },
              "attributes": [
                {
                  "key": "prompt",
                  "value": {
                    "stringValue": "Reply with exactly: GEMINI_NUMERIC_INT"
                  }
                }
              ]
            }
          ]
        }
      ]
    }
  ]
}`)

	events, err := DecodeAndNormalize(body, Context{})
	if err != nil {
		t.Fatalf("DecodeAndNormalize() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	assertEvent(t, events[0], core.EventLLMRequest, core.ProviderGemini, "Architect", "123")
	assertEvent(t, events[1], core.EventAgentBusy, core.ProviderGemini, "Architect", "123")
}

func TestSummarizeOutputMessages_AcceptsWrappedObject(t *testing.T) {
	summary, err := summarizeOutputMessages(`{
  "messages": [
    {
      "parts": [
        {
          "text": "WRAPPED_GEMINI_OK"
        }
      ]
    }
  ]
}`)
	if err != nil {
		t.Fatalf("summarizeOutputMessages() error = %v", err)
	}
	if !summary.HasVisibleText {
		t.Fatal("expected visible text to be detected")
	}
	if summary.Text != "WRAPPED_GEMINI_OK" {
		t.Fatalf("expected wrapped text, got %q", summary.Text)
	}
}

func mustReadFixture(t *testing.T, relative string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(relative))
	if err != nil {
		t.Fatalf("read fixture %s: %v", relative, err)
	}
	return body
}

func assertEvent(t *testing.T, event core.Event, kind core.EventKind, provider core.Provider, role string, sessionID string) {
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
}

func assertPayloadField(t *testing.T, event core.Event, key string, want string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	got, _ := payload[key].(string)
	if got != want {
		t.Fatalf("expected payload[%q] = %q, got %q", key, want, got)
	}
}
