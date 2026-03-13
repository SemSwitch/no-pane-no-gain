package tts

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/store"
)

type fakeBackend struct {
	name  string
	mu    sync.Mutex
	calls []string
	err   error
}

func (b *fakeBackend) Name() string { return b.name }

func (b *fakeBackend) Speak(_ context.Context, request Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, request.Text)
	return b.err
}

func TestWorkerProcessesQueuedJob(t *testing.T) {
	t.Parallel()

	database := openTTSTestStore(t)
	t.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC()
	if err := database.UpsertCapture(context.Background(), store.Capture{
		ID:          "cap-1",
		RepoRoot:    `C:\repo`,
		RepoHash:    "repohash",
		Name:        "builder",
		Provider:    "codex",
		Mode:        "normal",
		Status:      "done",
		CommandJSON: `["codex"]`,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("UpsertCapture: %v", err)
	}

	event, inserted, err := database.RecordEvent(context.Background(), store.EventInput{
		CaptureID:     "cap-1",
		RequestID:     "req-1",
		Kind:          "llm.response",
		Provider:      "codex",
		OccurredAt:    now,
		DedupeKey:     "event-1",
		PayloadJSON:   `{"assistant_text":"hello world"}`,
		AssistantText: "hello world",
		Status:        "done",
	})
	if err != nil || !inserted {
		t.Fatalf("RecordEvent inserted=%v err=%v", inserted, err)
	}

	if _, inserted, err := database.CreateTTSJob(context.Background(), store.TTSJobInput{
		CaptureID:   "cap-1",
		RequestID:   "req-1",
		EventID:     event.ID,
		Mode:        "normal",
		Backend:     "fake",
		Status:      "queued",
		SpeechText:  "hello world",
		SummaryText: "hello world",
		DedupeKey:   "job-1",
		QueuedAt:    now,
	}); err != nil || !inserted {
		t.Fatalf("CreateTTSJob inserted=%v err=%v", inserted, err)
	}

	backend := &fakeBackend{name: "fake"}
	registry := &Registry{
		defaults: Config{DefaultBackend: "fake"},
		backends: map[string]Backend{"fake": backend},
	}
	worker := NewWorker(database, nil, registry, "fake", 5*time.Millisecond, time.Second)

	if err := worker.ProcessPending(context.Background()); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}

	jobs, err := database.ListJobs(context.Background(), "done", 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != "done" {
		t.Fatalf("jobs = %+v", jobs)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.calls) != 1 || backend.calls[0] != "hello world" {
		t.Fatalf("backend calls = %+v", backend.calls)
	}
}

func openTTSTestStore(t *testing.T) *store.Store {
	t.Helper()

	database, err := store.Open(filepath.Join(t.TempDir(), "echo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return database
}
