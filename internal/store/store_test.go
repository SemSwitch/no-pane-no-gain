package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreateTTSJobAndLeaseFlow(t *testing.T) {
	t.Parallel()

	database := openTestStore(t)
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	if err := database.UpsertCapture(ctx, Capture{
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

	event, inserted, err := database.RecordEvent(ctx, EventInput{
		CaptureID:     "cap-1",
		RequestID:     "req-1",
		Kind:          "llm.response",
		Provider:      "codex",
		OccurredAt:    now,
		DedupeKey:     "event-1",
		PayloadJSON:   `{"text":"done"}`,
		AssistantText: "done",
		Status:        "running",
	})
	if err != nil || !inserted {
		t.Fatalf("RecordEvent inserted=%v err=%v", inserted, err)
	}

	job, inserted, err := database.CreateTTSJob(ctx, TTSJobInput{
		CaptureID:   "cap-1",
		RequestID:   "req-1",
		EventID:     event.ID,
		Mode:        "normal",
		Backend:     "windows",
		Status:      "queued",
		SpeechText:  "done",
		SummaryText: "done",
		DedupeKey:   "job-1",
		QueuedAt:    now,
	})
	if err != nil || !inserted {
		t.Fatalf("CreateTTSJob inserted=%v err=%v", inserted, err)
	}

	jobAgain, inserted, err := database.CreateTTSJob(ctx, TTSJobInput{
		CaptureID:   "cap-1",
		RequestID:   "req-1",
		EventID:     event.ID,
		Mode:        "normal",
		Backend:     "windows",
		Status:      "queued",
		SpeechText:  "done",
		SummaryText: "done",
		DedupeKey:   "job-1",
		QueuedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateTTSJob duplicate: %v", err)
	}
	if inserted {
		t.Fatalf("duplicate job should not insert")
	}
	if jobAgain.ID != job.ID {
		t.Fatalf("duplicate job returned id %d, want %d", jobAgain.ID, job.ID)
	}

	acquired, err := database.AcquireLease(ctx, "speaker", "owner-a", 2*time.Second)
	if err != nil || !acquired {
		t.Fatalf("AcquireLease first=%v err=%v", acquired, err)
	}
	acquired, err = database.AcquireLease(ctx, "speaker", "owner-b", 2*time.Second)
	if err != nil {
		t.Fatalf("AcquireLease second: %v", err)
	}
	if acquired {
		t.Fatalf("expected lease contention")
	}

	claimed, found, err := database.ClaimNextQueuedJob(ctx)
	if err != nil || !found {
		t.Fatalf("ClaimNextQueuedJob found=%v err=%v", found, err)
	}
	if claimed.ID != job.ID || claimed.Status != "speaking" {
		t.Fatalf("claimed job = %+v", claimed)
	}
	if err := database.MarkJobDone(ctx, claimed.ID); err != nil {
		t.Fatalf("MarkJobDone: %v", err)
	}

	jobs, err := database.ListJobs(ctx, "done", 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Status != "done" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	database, err := Open(filepath.Join(t.TempDir(), "echo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return database
}
