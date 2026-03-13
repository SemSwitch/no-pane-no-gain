package speech

import (
	"strings"
	"testing"
)

func TestSummarizerUsesConfiguredLimits(t *testing.T) {
	t.Parallel()

	summarizer := NewSummarizer(Config{
		Provider:        "heuristic",
		ConciseMaxChars: 24,
		NormalMaxChars:  40,
	})

	concise := summarizer.Build("concise", "This is a very long response that should be shortened for concise mode.")
	if concise.SpeechText == "" {
		t.Fatal("expected concise speech text")
	}
	if len(concise.SpeechText) > 24 {
		t.Fatalf("expected concise limit to apply, got %d chars: %q", len(concise.SpeechText), concise.SpeechText)
	}

	heavy := summarizer.Build("heavy", "Line one. Line two. Line three. Line four.")
	if heavy.SpeechText == "" {
		t.Fatal("expected heavy speech text")
	}
	if heavy.SummaryText == "" {
		t.Fatal("expected heavy summary text")
	}
	if len(heavy.SummaryText) > 40 {
		t.Fatalf("expected heavy summary limit to apply, got %d chars: %q", len(heavy.SummaryText), heavy.SummaryText)
	}
}

func TestProviderDefaultsToHeuristic(t *testing.T) {
	t.Parallel()

	if provider := NewSummarizer(Config{}).Provider(); provider != "heuristic" {
		t.Fatalf("expected default provider heuristic, got %q", provider)
	}

	if provider := NewSummarizer(Config{Provider: "  custom "}).Provider(); provider != "custom" {
		t.Fatalf("expected trimmed provider, got %q", provider)
	}

	if !strings.EqualFold(NewSummarizer(Config{Provider: "Heuristic"}).Provider(), "heuristic") {
		t.Fatal("expected provider normalization")
	}
}
