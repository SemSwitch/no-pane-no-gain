package echo

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

type CaptureID struct {
	Key      string
	Name     string
	RepoRoot string
}

func NewCaptureID(repoRoot string, name string) CaptureID {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(repoRoot)) + "|" + strings.TrimSpace(name)))
	return CaptureID{
		Key:      hex.EncodeToString(sum[:]),
		Name:     strings.TrimSpace(name),
		RepoRoot: strings.TrimSpace(repoRoot),
	}
}
