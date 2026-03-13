package store

import (
	"database/sql"
	"fmt"
)

func initSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS captures (
  id TEXT PRIMARY KEY,
  repo_root TEXT NOT NULL,
  repo_hash TEXT NOT NULL,
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  mode TEXT NOT NULL,
  status TEXT NOT NULL,
  command_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_event_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_captures_repo_hash_name
  ON captures(repo_hash, name);

CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  capture_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  provider TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  assistant_text TEXT,
  status TEXT,
  final INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_events_dedupe_key
  ON events(dedupe_key);

CREATE INDEX IF NOT EXISTS idx_events_capture_request_kind_time
  ON events(capture_id, request_id, kind, occurred_at, id);

CREATE TABLE IF NOT EXISTS tts_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  capture_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  event_id INTEGER,
  mode TEXT NOT NULL,
  backend TEXT NOT NULL,
  status TEXT NOT NULL,
  speech_text TEXT NOT NULL,
  summary_text TEXT,
  dedupe_key TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  queued_at TEXT NOT NULL,
  started_at TEXT,
  completed_at TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tts_jobs_dedupe_key
  ON tts_jobs(dedupe_key);

CREATE INDEX IF NOT EXISTS idx_tts_jobs_status_queued_at
  ON tts_jobs(status, queued_at, id);

CREATE TABLE IF NOT EXISTS leases (
  name TEXT PRIMARY KEY,
  owner_id TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL
);
`

	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}

	return nil
}
