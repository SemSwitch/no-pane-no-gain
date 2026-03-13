package speech

import "strings"

type Config struct {
	Provider        string
	ConciseMaxChars int
	NormalMaxChars  int
}

type Summarizer struct {
	provider        string
	conciseMaxChars int
	normalMaxChars  int
}

func NewSummarizer(cfg Config) *Summarizer {
	concise := cfg.ConciseMaxChars
	if concise <= 0 {
		concise = defaultConciseLimit
	}
	normal := cfg.NormalMaxChars
	if normal <= 0 {
		normal = defaultNormalLimit
	}

	return &Summarizer{
		provider:        strings.ToLower(strings.TrimSpace(cfg.Provider)),
		conciseMaxChars: concise,
		normalMaxChars:  normal,
	}
}

func (s *Summarizer) Build(mode string, assistantText string) Result {
	clean := Clean(assistantText)
	if clean == "" {
		return Result{}
	}

	switch normalizeMode(mode) {
	case "heavy":
		return Result{
			SpeechText:  clean,
			SummaryText: truncate(clean, s.normalMaxChars),
		}
	case "concise":
		short := firstSentences(clean, 1)
		if short == "" {
			short = truncate(clean, s.conciseMaxChars)
		}
		return Result{
			SpeechText:  truncate(short, s.conciseMaxChars),
			SummaryText: truncate(short, s.conciseMaxChars),
		}
	default:
		normal := firstSentences(clean, 3)
		if normal == "" {
			normal = truncate(clean, s.normalMaxChars)
		}
		return Result{
			SpeechText:  truncate(normal, s.normalMaxChars),
			SummaryText: truncate(normal, s.normalMaxChars),
		}
	}
}

func (s *Summarizer) Provider() string {
	if s == nil || s.provider == "" {
		return "heuristic"
	}
	return s.provider
}
