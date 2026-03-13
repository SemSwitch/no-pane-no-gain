package store

import "time"

type Capture struct {
	ID          string
	RepoRoot    string
	RepoHash    string
	Name        string
	Provider    string
	Mode        string
	Status      string
	CommandJSON string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastEventAt time.Time
}

type EventRecord struct {
	ID            int64
	CaptureID     string
	RequestID     string
	Kind          string
	Provider      string
	OccurredAt    time.Time
	DedupeKey     string
	PayloadJSON   string
	AssistantText string
	Status        string
	Final         bool
}

type EventInput struct {
	CaptureID     string
	RequestID     string
	Kind          string
	Provider      string
	OccurredAt    time.Time
	DedupeKey     string
	PayloadJSON   string
	AssistantText string
	Status        string
	Final         bool
}

type TTSJob struct {
	ID          int64
	CaptureID   string
	RequestID   string
	EventID     int64
	Mode        string
	Backend     string
	Status      string
	SpeechText  string
	SummaryText string
	DedupeKey   string
	Attempts    int
	LastError   string
	QueuedAt    time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

type TTSJobInput struct {
	CaptureID   string
	RequestID   string
	EventID     int64
	Mode        string
	Backend     string
	Status      string
	SpeechText  string
	SummaryText string
	DedupeKey   string
	QueuedAt    time.Time
}
