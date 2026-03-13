package echo

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/home"
)

type RunOptions struct {
	Paths               home.Paths
	CaptureName         string
	Provider            core.Provider
	Mode                string
	RepoRoot            string
	Args                []string
	ProviderArgs        []string
	Backend             string
	WindowsVoice        string
	GoogleVoice         string
	GoogleAPIKey        string
	LeaseTTL            time.Duration
	CompletionGrace     time.Duration
	SpeakerPollInterval time.Duration
	ProviderCommandPath string
	Logger              *slog.Logger
}

func (o *RunOptions) normalize() error {
	o.CaptureName = strings.TrimSpace(o.CaptureName)
	o.Provider = core.Provider(strings.ToLower(strings.TrimSpace(string(o.Provider))))
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	o.Backend = strings.ToLower(strings.TrimSpace(o.Backend))
	if len(o.ProviderArgs) == 0 && len(o.Args) > 0 {
		o.ProviderArgs = append([]string(nil), o.Args...)
	}

	switch o.Mode {
	case "", "normal":
		o.Mode = "normal"
	case "concise", "heavy":
	default:
		return fmt.Errorf("unsupported mode %q", o.Mode)
	}

	switch o.Backend {
	case "", "windows":
		o.Backend = "windows"
	case "google":
	default:
		return fmt.Errorf("unsupported tts backend %q", o.Backend)
	}

	if o.CaptureName == "" {
		return fmt.Errorf("capture name must not be empty")
	}
	if _, err := core.ParseProvider(string(o.Provider)); err != nil {
		return err
	}
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = 15 * time.Second
	}
	if o.CompletionGrace <= 0 {
		o.CompletionGrace = 3 * time.Second
	}
	if o.SpeakerPollInterval <= 0 {
		o.SpeakerPollInterval = 500 * time.Millisecond
	}

	return nil
}
