package tts

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/speech"
	"github.com/SemSwitch/Nexis-Echo/internal/store"
)

const speakerLease = "speaker"

type Worker struct {
	store    *store.Store
	logger   *slog.Logger
	registry *Registry
	backend  string
	ownerID  string
	poll     time.Duration
	leaseTTL time.Duration
}

func NewWorker(database *store.Store, logger *slog.Logger, registry *Registry, backend string, poll time.Duration, leaseTTL time.Duration) *Worker {
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	if leaseTTL <= 0 {
		leaseTTL = 15 * time.Second
	}
	return &Worker{
		store:    database,
		logger:   logger,
		registry: registry,
		backend:  strings.ToLower(strings.TrimSpace(backend)),
		ownerID:  fmt.Sprintf("pid-%d", os.Getpid()),
		poll:     poll,
		leaseTTL: leaseTTL,
	}
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = w.store.ReleaseLease(context.Background(), speakerLease, w.ownerID)
			return nil
		case <-ticker.C:
			if _, err := w.processOne(ctx); err != nil && w.logger != nil {
				w.logger.Warn("tts worker iteration failed", "error", err)
			}
		}
	}
}

func (w *Worker) ProcessPending(ctx context.Context) error {
	for {
		processed, err := w.processOne(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
}

func (w *Worker) processOne(ctx context.Context) (bool, error) {
	if w.store == nil || w.registry == nil {
		return false, nil
	}

	acquired, err := w.store.AcquireLease(ctx, speakerLease, w.ownerID, w.leaseTTL)
	if err != nil || !acquired {
		return false, err
	}
	defer func() {
		_ = w.store.ReleaseLease(context.Background(), speakerLease, w.ownerID)
	}()

	job, found, err := w.store.ClaimNextQueuedJob(ctx)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	backend := strings.TrimSpace(job.Backend)
	if backend == "" {
		backend = w.backend
	}
	if backend == "" {
		backend = w.registry.DefaultBackend()
	}

	limit := 3500
	if strings.EqualFold(backend, "google") {
		limit = 4500
	}
	chunks := speech.Chunk(job.SpeechText, limit)
	if len(chunks) == 0 {
		chunks = []string{strings.TrimSpace(job.SpeechText)}
	}

	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		if err := w.registry.Speak(ctx, backend, Request{Text: chunk}); err != nil {
			_ = w.store.MarkJobFailed(context.Background(), job.ID, err)
			return true, nil
		}
	}

	if err := w.store.MarkJobDone(ctx, job.ID); err != nil {
		return false, err
	}
	return true, nil
}
