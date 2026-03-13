package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultJobListLimit = 100
	timeLayout          = time.RFC3339Nano
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite dir: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;
`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite pragmas: %w", err)
	}

	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) UpsertCapture(ctx context.Context, capture Capture) error {
	now := utcOrNow(capture.UpdatedAt)
	createdAt := utcOrNow(capture.CreatedAt)
	lastEventAt := nullableTimeString(capture.LastEventAt)

	_, err := s.db.ExecContext(ctx, `
INSERT INTO captures (
  id, repo_root, repo_hash, name, provider, mode, status, command_json,
  created_at, updated_at, last_event_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  repo_root = excluded.repo_root,
  repo_hash = excluded.repo_hash,
  name = excluded.name,
  provider = excluded.provider,
  mode = excluded.mode,
  status = excluded.status,
  command_json = excluded.command_json,
  updated_at = excluded.updated_at,
  last_event_at = excluded.last_event_at
`, capture.ID, capture.RepoRoot, capture.RepoHash, capture.Name, capture.Provider, capture.Mode,
		defaultString(capture.Status, "idle"), capture.CommandJSON, createdAt.Format(timeLayout), now.Format(timeLayout), lastEventAt)
	if err != nil {
		return fmt.Errorf("upsert capture: %w", err)
	}

	return nil
}

func (s *Store) UpdateCaptureState(ctx context.Context, captureID string, status string, lastEventAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE captures
SET status = ?, updated_at = ?, last_event_at = ?
WHERE id = ?
`, defaultString(status, "idle"), time.Now().UTC().Format(timeLayout), nullableTimeString(lastEventAt), captureID)
	if err != nil {
		return fmt.Errorf("update capture state: %w", err)
	}
	return nil
}

func (s *Store) RecordEvent(ctx context.Context, input EventInput) (EventRecord, bool, error) {
	if input.DedupeKey == "" {
		return EventRecord{}, false, errors.New("event dedupe key must not be empty")
	}
	occurredAt := utcOrNow(input.OccurredAt)

	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO events (
  capture_id, request_id, kind, provider, occurred_at, dedupe_key, payload_json,
  assistant_text, status, final
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, input.CaptureID, input.RequestID, input.Kind, input.Provider, occurredAt.Format(timeLayout), input.DedupeKey,
		input.PayloadJSON, nullableString(input.AssistantText), nullableString(input.Status), boolToInt(input.Final))
	if err != nil {
		return EventRecord{}, false, fmt.Errorf("insert event: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return EventRecord{}, false, fmt.Errorf("event rows affected: %w", err)
	}

	record, err := s.eventByDedupeKey(ctx, input.DedupeKey)
	if err != nil {
		return EventRecord{}, false, err
	}

	return record, rows > 0, nil
}

func (s *Store) LatestAssistantEvent(ctx context.Context, captureID string, requestID string) (EventRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, capture_id, request_id, kind, provider, occurred_at, dedupe_key, payload_json,
       COALESCE(assistant_text, ''), COALESCE(status, ''), final
FROM events
WHERE capture_id = ? AND request_id = ? AND kind = 'llm.response' AND COALESCE(assistant_text, '') <> ''
ORDER BY final DESC, occurred_at DESC, id DESC
LIMIT 1
`, captureID, requestID)

	record, found, err := scanEventRecord(row)
	if err != nil || !found {
		return EventRecord{}, found, err
	}
	return record, true, nil
}

func (s *Store) CreateTTSJob(ctx context.Context, input TTSJobInput) (TTSJob, bool, error) {
	if input.DedupeKey == "" {
		return TTSJob{}, false, errors.New("tts job dedupe key must not be empty")
	}
	if input.Status == "" {
		input.Status = "queued"
	}
	queuedAt := utcOrNow(input.QueuedAt)

	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO tts_jobs (
  capture_id, request_id, event_id, mode, backend, status, speech_text, summary_text,
  dedupe_key, queued_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, input.CaptureID, input.RequestID, nullableInt64(input.EventID), input.Mode, input.Backend,
		input.Status, input.SpeechText, nullableString(input.SummaryText), input.DedupeKey, queuedAt.Format(timeLayout))
	if err != nil {
		return TTSJob{}, false, fmt.Errorf("insert tts job: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return TTSJob{}, false, fmt.Errorf("tts job rows affected: %w", err)
	}

	job, err := s.jobByDedupeKey(ctx, input.DedupeKey)
	if err != nil {
		return TTSJob{}, false, err
	}

	return job, rows > 0, nil
}

func (s *Store) ListCaptures(ctx context.Context) ([]Capture, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, repo_root, repo_hash, name, provider, mode, status, command_json,
       created_at, updated_at, COALESCE(last_event_at, '')
FROM captures
ORDER BY updated_at DESC, name ASC
`)
	if err != nil {
		return nil, fmt.Errorf("query captures: %w", err)
	}
	defer rows.Close()

	var out []Capture
	for rows.Next() {
		var capture Capture
		var createdAt string
		var updatedAt string
		var lastEventAt string
		if err := rows.Scan(&capture.ID, &capture.RepoRoot, &capture.RepoHash, &capture.Name, &capture.Provider,
			&capture.Mode, &capture.Status, &capture.CommandJSON, &createdAt, &updatedAt, &lastEventAt); err != nil {
			return nil, fmt.Errorf("scan capture: %w", err)
		}
		capture.CreatedAt = parseStoredTime(createdAt)
		capture.UpdatedAt = parseStoredTime(updatedAt)
		capture.LastEventAt = parseStoredTime(lastEventAt)
		out = append(out, capture)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate captures: %w", err)
	}
	return out, nil
}

func (s *Store) ListJobs(ctx context.Context, status string, limit int) ([]TTSJob, error) {
	if limit <= 0 {
		limit = defaultJobListLimit
	}

	query := `
SELECT id, capture_id, request_id, COALESCE(event_id, 0), mode, backend, status,
       speech_text, COALESCE(summary_text, ''), dedupe_key, attempts,
       COALESCE(last_error, ''), queued_at, COALESCE(started_at, ''), COALESCE(completed_at, '')
FROM tts_jobs
`
	args := []any{}
	if status != "" {
		query += "WHERE status = ? "
		args = append(args, status)
	}
	query += "ORDER BY queued_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query tts jobs: %w", err)
	}
	defer rows.Close()

	var out []TTSJob
	for rows.Next() {
		job, found, err := scanTTSJob(rows)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, job)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tts jobs: %w", err)
	}
	return out, nil
}

func (s *Store) RetryJob(ctx context.Context, jobID int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tts_jobs
SET status = 'queued',
    started_at = NULL,
    completed_at = NULL,
    last_error = NULL
WHERE id = ?
`, jobID)
	if err != nil {
		return fmt.Errorf("retry tts job: %w", err)
	}
	return nil
}

func (s *Store) AcquireLease(ctx context.Context, name string, ownerID string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin lease tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentOwner string
	var currentExpiry string
	err = tx.QueryRowContext(ctx, `SELECT owner_id, expires_at FROM leases WHERE name = ?`, name).Scan(&currentOwner, &currentExpiry)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO leases(name, owner_id, expires_at) VALUES (?, ?, ?)`,
			name, ownerID, expiresAt.Format(timeLayout)); err != nil {
			return false, fmt.Errorf("insert lease: %w", err)
		}
	case err != nil:
		return false, fmt.Errorf("query lease: %w", err)
	default:
		currentTime := parseStoredTime(currentExpiry)
		if currentOwner != ownerID && currentTime.After(now) {
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("commit rejected lease tx: %w", err)
			}
			return false, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE leases SET owner_id = ?, expires_at = ? WHERE name = ?`,
			ownerID, expiresAt.Format(timeLayout), name); err != nil {
			return false, fmt.Errorf("update lease: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit lease tx: %w", err)
	}
	return true, nil
}

func (s *Store) ReleaseLease(ctx context.Context, name string, ownerID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM leases WHERE name = ? AND owner_id = ?`, name, ownerID)
	if err != nil {
		return fmt.Errorf("release lease: %w", err)
	}
	return nil
}

func (s *Store) ResetInFlightJobs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tts_jobs
SET status = 'queued',
    started_at = NULL
WHERE status = 'speaking'
`)
	if err != nil {
		return fmt.Errorf("reset in-flight tts jobs: %w", err)
	}
	return nil
}

func (s *Store) ClaimNextQueuedJob(ctx context.Context) (TTSJob, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TTSJob{}, false, fmt.Errorf("begin claim job tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
SELECT id, capture_id, request_id, COALESCE(event_id, 0), mode, backend, status,
       speech_text, COALESCE(summary_text, ''), dedupe_key, attempts,
       COALESCE(last_error, ''), queued_at, COALESCE(started_at, ''), COALESCE(completed_at, '')
FROM tts_jobs
WHERE status = 'queued'
ORDER BY queued_at ASC, id ASC
LIMIT 1
`)
	job, found, err := scanTTSJob(row)
	if err != nil {
		return TTSJob{}, false, err
	}
	if !found {
		if err := tx.Commit(); err != nil {
			return TTSJob{}, false, fmt.Errorf("commit empty claim job tx: %w", err)
		}
		return TTSJob{}, false, nil
	}

	startedAt := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
UPDATE tts_jobs
SET status = 'speaking',
    started_at = ?,
    attempts = attempts + 1
WHERE id = ? AND status = 'queued'
`, startedAt.Format(timeLayout), job.ID)
	if err != nil {
		return TTSJob{}, false, fmt.Errorf("update claimed tts job: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return TTSJob{}, false, fmt.Errorf("claimed tts job rows affected: %w", err)
	}
	if rows == 0 {
		if err := tx.Commit(); err != nil {
			return TTSJob{}, false, fmt.Errorf("commit lost race claim job tx: %w", err)
		}
		return TTSJob{}, false, nil
	}

	if err := tx.Commit(); err != nil {
		return TTSJob{}, false, fmt.Errorf("commit claim job tx: %w", err)
	}

	job.Status = "speaking"
	job.StartedAt = startedAt
	job.Attempts++
	return job, true, nil
}

func (s *Store) MarkJobDone(ctx context.Context, jobID int64) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tts_jobs
SET status = 'done',
    completed_at = ?,
    last_error = NULL
WHERE id = ?
`, time.Now().UTC().Format(timeLayout), jobID)
	if err != nil {
		return fmt.Errorf("mark tts job done: %w", err)
	}
	return nil
}

func (s *Store) MarkJobFailed(ctx context.Context, jobID int64, failure error) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tts_jobs
SET status = 'failed',
    completed_at = ?,
    last_error = ?
WHERE id = ?
`, time.Now().UTC().Format(timeLayout), errorString(failure), jobID)
	if err != nil {
		return fmt.Errorf("mark tts job failed: %w", err)
	}
	return nil
}

func (s *Store) SetSetting(ctx context.Context, key string, valueJSON string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings(key, value_json) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json
`, key, valueJSON)
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get setting: %w", err)
	}
	return value, true, nil
}

func (s *Store) eventByDedupeKey(ctx context.Context, dedupeKey string) (EventRecord, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, capture_id, request_id, kind, provider, occurred_at, dedupe_key, payload_json,
       COALESCE(assistant_text, ''), COALESCE(status, ''), final
FROM events
WHERE dedupe_key = ?
`, dedupeKey)
	record, found, err := scanEventRecord(row)
	if err != nil {
		return EventRecord{}, err
	}
	if !found {
		return EventRecord{}, fmt.Errorf("event %q not found after insert", dedupeKey)
	}
	return record, nil
}

func (s *Store) jobByDedupeKey(ctx context.Context, dedupeKey string) (TTSJob, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, capture_id, request_id, COALESCE(event_id, 0), mode, backend, status,
       speech_text, COALESCE(summary_text, ''), dedupe_key, attempts,
       COALESCE(last_error, ''), queued_at, COALESCE(started_at, ''), COALESCE(completed_at, '')
FROM tts_jobs
WHERE dedupe_key = ?
`, dedupeKey)
	job, found, err := scanTTSJob(row)
	if err != nil {
		return TTSJob{}, err
	}
	if !found {
		return TTSJob{}, fmt.Errorf("tts job %q not found after insert", dedupeKey)
	}
	return job, nil
}

func scanEventRecord(scanner interface{ Scan(...any) error }) (EventRecord, bool, error) {
	var record EventRecord
	var occurredAt string
	var final int
	err := scanner.Scan(&record.ID, &record.CaptureID, &record.RequestID, &record.Kind, &record.Provider,
		&occurredAt, &record.DedupeKey, &record.PayloadJSON, &record.AssistantText, &record.Status, &final)
	if errors.Is(err, sql.ErrNoRows) {
		return EventRecord{}, false, nil
	}
	if err != nil {
		return EventRecord{}, false, fmt.Errorf("scan event record: %w", err)
	}
	record.OccurredAt = parseStoredTime(occurredAt)
	record.Final = final == 1
	return record, true, nil
}

func scanTTSJob(scanner interface{ Scan(...any) error }) (TTSJob, bool, error) {
	var job TTSJob
	var queuedAt string
	var startedAt string
	var completedAt string
	err := scanner.Scan(&job.ID, &job.CaptureID, &job.RequestID, &job.EventID, &job.Mode, &job.Backend,
		&job.Status, &job.SpeechText, &job.SummaryText, &job.DedupeKey, &job.Attempts,
		&job.LastError, &queuedAt, &startedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TTSJob{}, false, nil
	}
	if err != nil {
		return TTSJob{}, false, fmt.Errorf("scan tts job: %w", err)
	}
	job.QueuedAt = parseStoredTime(queuedAt)
	job.StartedAt = parseStoredTime(startedAt)
	job.CompletedAt = parseStoredTime(completedAt)
	return job, true, nil
}

func utcOrNow(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func parseStoredTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(timeLayout, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTimeString(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(timeLayout)
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
