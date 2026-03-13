package speech

import (
	"regexp"
	"strings"
)

const (
	defaultConciseLimit = 220
	defaultNormalLimit  = 650
)

type Result struct {
	SpeechText  string
	SummaryText string
}

var (
	whitespacePattern = regexp.MustCompile(`\s+`)
	bulletPattern     = regexp.MustCompile(`(?m)^\s*[-*]\s+`)
	headingPattern    = regexp.MustCompile(`(?m)^\s*#+\s*`)
	codeFencePattern  = regexp.MustCompile("(?s)```.+?```")
	inlineCodePattern = regexp.MustCompile("`([^`]+)`")
)

func Build(mode string, assistantText string) Result {
	return NewSummarizer(Config{}).Build(mode, assistantText)
}

func Clean(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = codeFencePattern.ReplaceAllStringFunc(value, func(block string) string {
		block = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(block, "```"), "```"))
		if block == "" {
			return ""
		}
		return " code block omitted "
	})
	value = inlineCodePattern.ReplaceAllString(value, "$1")
	value = headingPattern.ReplaceAllString(value, "")
	value = bulletPattern.ReplaceAllString(value, "")
	value = strings.NewReplacer(
		"|", " ",
		"*", " ",
		"_", " ",
		"#", " ",
		">", " ",
	).Replace(value)
	value = whitespacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func Chunk(value string, limit int) []string {
	value = Clean(value)
	if value == "" {
		return nil
	}
	if limit <= 0 || len(value) <= limit {
		return []string{value}
	}

	parts := []string{}
	for len(value) > 0 {
		if len(value) <= limit {
			parts = append(parts, strings.TrimSpace(value))
			break
		}
		cut := strings.LastIndexAny(value[:limit], ".!?;")
		if cut < limit/2 {
			cut = strings.LastIndexByte(value[:limit], ' ')
		}
		if cut <= 0 {
			cut = limit
		}
		parts = append(parts, strings.TrimSpace(value[:cut]))
		value = strings.TrimSpace(value[cut:])
	}
	return parts
}

func normalizeMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "concise":
		return "concise"
	case "heavy", "high":
		return "heavy"
	default:
		return "normal"
	}
}

func firstSentences(value string, count int) string {
	if count <= 0 {
		return ""
	}
	sentences := strings.FieldsFunc(value, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})
	if len(sentences) == 0 {
		return ""
	}
	if len(sentences) > count {
		sentences = sentences[:count]
	}

	out := make([]string, 0, len(sentences))
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		out = append(out, sentence+".")
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return strings.TrimSpace(value[:limit-3]) + "..."
}
