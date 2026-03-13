package core

import (
	"reflect"
	"testing"
	"time"
)

func TestSubjectForEvent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name:  "registry online",
			event: Event{Kind: EventRegistryOnline, Timestamp: now},
			want:  SubjectRegistryOnline,
		},
		{
			name:  "registry offline",
			event: Event{Kind: EventRegistryOffline, Timestamp: now},
			want:  SubjectRegistryOffline,
		},
		{
			name:  "agent busy",
			event: Event{Kind: EventAgentBusy, Role: "builder", Timestamp: now},
			want:  SubjectAgentState + ".builder",
		},
		{
			name:  "agent error with role",
			event: Event{Kind: EventAgentError, Role: "reviewer", Timestamp: now},
			want:  SubjectSystemError + ".reviewer",
		},
		{
			name:  "agent error without role",
			event: Event{Kind: EventAgentError, Timestamp: now},
			want:  SubjectSystemError,
		},
		{
			name: "llm request",
			event: Event{
				Kind:      EventLLMRequest,
				Provider:  ProviderCodex,
				Role:      "architect",
				Timestamp: now,
			},
			want: SubjectLLM + ".codex.architect.request",
		},
		{
			name: "llm response",
			event: Event{
				Kind:      EventLLMResponse,
				Provider:  ProviderClaude,
				Role:      "builder",
				Timestamp: now,
			},
			want: SubjectLLM + ".claude.builder.response",
		},
		{
			name: "tool call",
			event: Event{
				Kind:      EventToolCall,
				Provider:  ProviderGemini,
				Role:      "tester",
				Timestamp: now,
			},
			want: SubjectTool + ".gemini.tester.call",
		},
		{
			name: "tool result",
			event: Event{
				Kind:      EventToolResult,
				Provider:  ProviderGemini,
				Role:      "tester",
				Timestamp: now,
			},
			want: SubjectTool + ".gemini.tester.result",
		},
		{
			name:  "unknown kind falls back to role scoped system error",
			event: Event{Kind: "mystery", Role: "planner", Timestamp: now},
			want:  SubjectSystemError + ".planner",
		},
		{
			name:  "unknown kind without role falls back to system error",
			event: Event{Kind: "mystery", Timestamp: now},
			want:  SubjectSystemError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SubjectForEvent(tt.event); got != tt.want {
				t.Fatalf("SubjectForEvent(%s) = %q, want %q", tt.event.Kind, got, tt.want)
			}
		})
	}
}

func TestStreamSubjects(t *testing.T) {
	t.Parallel()

	want := []string{
		SubjectRegistryOnline,
		SubjectRegistryOffline,
		SubjectAgentState + ".>",
		SubjectLLM + ".>",
		SubjectTool + ".>",
		SubjectTaskTrigger,
		SubjectSystemError + ".>",
	}

	if got := StreamSubjects(); !reflect.DeepEqual(got, want) {
		t.Fatalf("StreamSubjects() = %#v, want %#v", got, want)
	}
}
