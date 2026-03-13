package echo

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/speech"
	"github.com/SemSwitch/Nexis-Echo/internal/store"
)

type processor struct {
	store           *store.Store
	logger          Logger
	captureID       string
	captureName     string
	mode            string
	backend         string
	summarizer      *speech.Summarizer
	completionGrace time.Duration
}

type Logger interface {
	Warn(string, ...any)
}

func (p *processor) HandleEvent(ctx context.Context, body []byte) error {
	var event core.Event
	if err := json.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("decode core event: %w", err)
	}
	if !matchesCapture(p.captureID, p.captureName, event) {
		return nil
	}

	assistantText := assistantTextForEvent(event)
	status := eventStatus(event)
	final := event.Kind == core.EventAgentDone

	record, inserted, err := p.store.RecordEvent(ctx, store.EventInput{
		CaptureID:     p.captureID,
		RequestID:     event.RequestID,
		Kind:          string(event.Kind),
		Provider:      string(event.Provider),
		OccurredAt:    event.Timestamp,
		DedupeKey:     eventDedupeKey(event),
		PayloadJSON:   payloadString(event.Payload),
		AssistantText: assistantText,
		Status:        status,
		Final:         final,
	})
	if err != nil {
		return err
	}
	if !inserted {
		return nil
	}

	if err := p.store.UpdateCaptureState(ctx, p.captureID, status, event.Timestamp); err != nil {
		return err
	}
	if event.Kind != core.EventAgentDone {
		return nil
	}

	response, found, err := waitForAssistantEvent(ctx, p.store, p.captureID, event.RequestID, p.completionGrace)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	summarizer := p.summarizer
	if summarizer == nil {
		summarizer = speech.NewSummarizer(speech.Config{})
	}
	result := summarizer.Build(p.mode, response.AssistantText)
	if strings.TrimSpace(result.SpeechText) == "" {
		return nil
	}

	_, _, err = p.store.CreateTTSJob(ctx, store.TTSJobInput{
		CaptureID:   p.captureID,
		RequestID:   event.RequestID,
		EventID:     chooseEventID(response.ID, record.ID),
		Mode:        p.mode,
		Backend:     p.backend,
		Status:      "queued",
		SpeechText:  result.SpeechText,
		SummaryText: result.SummaryText,
		DedupeKey:   ttsJobDedupeKey(p.captureID, event.RequestID, p.mode, p.backend),
		QueuedAt:    time.Now().UTC(),
	})
	return err
}

func waitForAssistantEvent(ctx context.Context, database *store.Store, captureID string, requestID string, timeout time.Duration) (store.EventRecord, bool, error) {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		record, found, err := database.LatestAssistantEvent(ctx, captureID, requestID)
		if err != nil || found {
			return record, found, err
		}
		if time.Now().After(deadline) {
			return store.EventRecord{}, false, nil
		}
		select {
		case <-ctx.Done():
			return store.EventRecord{}, false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func eventStatus(event core.Event) string {
	switch event.Kind {
	case core.EventAgentDone:
		return "done"
	case core.EventAgentError:
		return "error"
	default:
		return "running"
	}
}

func assistantTextForEvent(event core.Event) string {
	if event.Kind != core.EventLLMResponse || len(event.Payload) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return ""
	}
	if text := textValue(payload["assistant_text"]); text != "" {
		return speech.Clean(text)
	}
	if text := textValue(payload["text"]); text != "" {
		return speech.Clean(text)
	}
	if body, ok := payload["body"]; ok {
		return speech.Clean(extractText(body))
	}
	return speech.Clean(extractText(payload))
}

func extractText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := extractText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		if text := textValue(typed["assistant_text"]); text != "" {
			return text
		}
		if text := textValue(typed["text"]); text != "" {
			return text
		}
		if content, ok := typed["content"]; ok {
			return extractText(content)
		}
		if output, ok := typed["output"]; ok {
			return extractText(output)
		}
		if messages, ok := typed["messages"]; ok {
			return extractText(messages)
		}
		parts := make([]string, 0, len(typed))
		for _, nested := range typed {
			if text := extractText(nested); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func textValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func matchesCapture(captureID string, captureName string, event core.Event) bool {
	if strings.TrimSpace(event.StableID) != "" {
		return strings.TrimSpace(event.StableID) == captureID
	}
	return strings.TrimSpace(event.Role) == captureName
}

func eventDedupeKey(event core.Event) string {
	metadata, _ := json.Marshal(event.Metadata)
	sum := sha1.Sum([]byte(strings.Join([]string{
		string(event.Kind),
		string(event.Provider),
		event.Role,
		event.StableID,
		event.RepoRoot,
		event.SessionID,
		event.RequestID,
		event.Timestamp.UTC().Format(time.RFC3339Nano),
		string(event.Payload),
		string(metadata),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func ttsJobDedupeKey(captureID string, requestID string, mode string, backend string) string {
	sum := sha1.Sum([]byte(strings.Join([]string{captureID, requestID, mode, backend}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func payloadString(payload json.RawMessage) string {
	if len(payload) == 0 {
		return "{}"
	}
	return string(payload)
}

func chooseEventID(primary int64, fallback int64) int64 {
	if primary > 0 {
		return primary
	}
	return fallback
}
