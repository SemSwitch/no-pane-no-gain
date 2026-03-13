package echo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/store"
)

func TestProcessorCreatesOneTTSJobOnAgentDone(t *testing.T) {
	t.Parallel()

	database := openEchoTestStore(t)
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	if err := database.UpsertCapture(context.Background(), store.Capture{
		ID:          "cap-1",
		RepoRoot:    `C:\repo`,
		RepoHash:    "repohash",
		Name:        "builder",
		Provider:    "codex",
		Mode:        "normal",
		Status:      "running",
		CommandJSON: `["codex"]`,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("UpsertCapture: %v", err)
	}

	proc := &processor{
		store:           database,
		captureID:       "cap-1",
		captureName:     "builder",
		mode:            "normal",
		backend:         "windows",
		completionGrace: time.Second,
	}

	responseBody := mustMarshalEvent(t, core.Event{
		Kind:      core.EventLLMResponse,
		Provider:  core.ProviderCodex,
		Role:      "builder",
		StableID:  "cap-1",
		RequestID: "req-1",
		Timestamp: now,
		Payload:   json.RawMessage(`{"assistant_text":"Build succeeded.\nAll tests passed."}`),
	})
	if err := proc.HandleEvent(context.Background(), responseBody); err != nil {
		t.Fatalf("HandleEvent response: %v", err)
	}

	doneBody := mustMarshalEvent(t, core.Event{
		Kind:      core.EventAgentDone,
		Provider:  core.ProviderCodex,
		Role:      "builder",
		StableID:  "cap-1",
		RequestID: "req-1",
		Timestamp: now.Add(time.Second),
		Payload:   json.RawMessage(`{"status":"done"}`),
	})
	if err := proc.HandleEvent(context.Background(), doneBody); err != nil {
		t.Fatalf("HandleEvent done: %v", err)
	}
	if err := proc.HandleEvent(context.Background(), doneBody); err != nil {
		t.Fatalf("HandleEvent duplicate done: %v", err)
	}

	jobs, err := database.ListJobs(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].RequestID != "req-1" || jobs[0].Backend != "windows" {
		t.Fatalf("job = %+v", jobs[0])
	}
	if jobs[0].EventID == 0 {
		t.Fatalf("expected response event id on tts job")
	}
	if jobs[0].SpeechText == "" || jobs[0].SummaryText == "" {
		t.Fatalf("expected speech text, got %+v", jobs[0])
	}
}

func TestProcessorIgnoresOtherCapture(t *testing.T) {
	t.Parallel()

	database := openEchoTestStore(t)
	t.Cleanup(func() { _ = database.Close() })

	proc := &processor{
		store:           database,
		captureID:       "cap-1",
		captureName:     "builder",
		mode:            "normal",
		backend:         "windows",
		completionGrace: time.Second,
	}

	body := mustMarshalEvent(t, core.Event{
		Kind:      core.EventLLMResponse,
		Provider:  core.ProviderCodex,
		Role:      "other",
		StableID:  "cap-2",
		RequestID: "req-2",
		Timestamp: time.Now().UTC(),
		Payload:   json.RawMessage(`{"assistant_text":"ignore me"}`),
	})
	if err := proc.HandleEvent(context.Background(), body); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	jobs, err := database.ListJobs(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}
}

func openEchoTestStore(t *testing.T) *store.Store {
	t.Helper()

	database, err := store.Open(filepath.Join(t.TempDir(), "echo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return database
}

func mustMarshalEvent(t *testing.T, event core.Event) []byte {
	t.Helper()

	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal event: %v", err)
	}
	return body
}
