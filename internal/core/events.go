package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
	ProviderGemini Provider = "gemini"
)

func ParseProvider(raw string) (Provider, error) {
	switch Provider(strings.ToLower(strings.TrimSpace(raw))) {
	case ProviderClaude:
		return ProviderClaude, nil
	case ProviderCodex:
		return ProviderCodex, nil
	case ProviderGemini:
		return ProviderGemini, nil
	default:
		return "", fmt.Errorf("unsupported provider %q", raw)
	}
}

type EventKind string

const (
	EventRegistryOnline  EventKind = "registry.online"
	EventRegistryOffline EventKind = "registry.offline"
	EventAgentWaiting    EventKind = "agent.waiting"
	EventAgentBusy       EventKind = "agent.busy"
	EventAgentTooling    EventKind = "agent.tooling"
	EventAgentDone       EventKind = "agent.done"
	EventAgentError      EventKind = "agent.error"
	EventLLMRequest      EventKind = "llm.request"
	EventLLMResponse     EventKind = "llm.response"
	EventToolCall        EventKind = "tool.call"
	EventToolResult      EventKind = "tool.result"
)

func ParseEventKind(raw string) (EventKind, error) {
	switch EventKind(strings.ToLower(strings.TrimSpace(raw))) {
	case EventRegistryOnline:
		return EventRegistryOnline, nil
	case EventRegistryOffline:
		return EventRegistryOffline, nil
	case EventAgentWaiting:
		return EventAgentWaiting, nil
	case EventAgentBusy:
		return EventAgentBusy, nil
	case EventAgentTooling:
		return EventAgentTooling, nil
	case EventAgentDone:
		return EventAgentDone, nil
	case EventAgentError:
		return EventAgentError, nil
	case EventLLMRequest:
		return EventLLMRequest, nil
	case EventLLMResponse:
		return EventLLMResponse, nil
	case EventToolCall:
		return EventToolCall, nil
	case EventToolResult:
		return EventToolResult, nil
	default:
		return "", fmt.Errorf("unsupported event kind %q", raw)
	}
}

func EventKinds() []EventKind {
	return []EventKind{
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
}

type SourceKind string

const (
	SourceWrapper    SourceKind = "wrapper"
	SourceGateway    SourceKind = "gateway"
	SourceOTLP       SourceKind = "otlp"
	SourceSessionLog SourceKind = "session_log"
)

type ContentMode string

const (
	ContentFull         ContentMode = "full"
	ContentMetadataOnly ContentMode = "metadata_only"
	ContentRedacted     ContentMode = "redacted"
)

type Sensitivity string

const (
	SensitivityLocalOnly Sensitivity = "local_only"
	SensitivityShareable Sensitivity = "shareable"
)

type Event struct {
	Kind        EventKind       `json:"kind"`
	Provider    Provider        `json:"provider"`
	Role        string          `json:"role"`
	StableID    string          `json:"stable_id,omitempty"`
	RepoRoot    string          `json:"repo_root,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	RequestID   string          `json:"request_id,omitempty"`
	Timestamp   time.Time       `json:"timestamp"`
	ContentMode ContentMode     `json:"content_mode"`
	Source      SourceKind      `json:"source"`
	Sensitivity Sensitivity     `json:"sensitivity"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Metadata    map[string]any  `json:"metadata,omitempty"`
}
