package core

import (
	"reflect"
	"testing"
)

func TestParseEventKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    EventKind
		wantErr bool
	}{
		{name: "registry online", raw: "registry.online", want: EventRegistryOnline},
		{name: "upper trims", raw: "  LLM.RESPONSE  ", want: EventLLMResponse},
		{name: "tool result", raw: "tool.result", want: EventToolResult},
		{name: "invalid", raw: "nope", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseEventKind(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseEventKind(%q) error = nil, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseEventKind(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ParseEventKind(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestEventKinds(t *testing.T) {
	t.Parallel()

	want := []EventKind{
		EventRegistryOnline,
		EventRegistryOffline,
		EventAgentWaiting,
		EventAgentBusy,
		EventAgentTooling,
		EventAgentDone,
		EventAgentError,
		EventLLMRequest,
		EventLLMResponse,
		EventToolCall,
		EventToolResult,
	}

	if got := EventKinds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("EventKinds() = %#v, want %#v", got, want)
	}
}
