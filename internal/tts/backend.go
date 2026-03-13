package tts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type Request struct {
	Text  string
	Voice string
}

type Backend interface {
	Name() string
	Speak(context.Context, Request) error
}

type Config struct {
	DefaultBackend string
	RuntimeDir     string
	WindowsVoice   string
	GoogleAPIKey   string
	GoogleVoice    string
}

type Registry struct {
	logger   *slog.Logger
	defaults Config
	backends map[string]Backend
}

func NewRegistry(cfg Config, logger *slog.Logger) *Registry {
	registry := &Registry{
		logger:   logger,
		defaults: cfg,
		backends: map[string]Backend{},
	}

	registry.register(NewWindows(cfg.WindowsVoice))
	if google := NewGoogle(GoogleConfig{
		APIKey:     cfg.GoogleAPIKey,
		Voice:      cfg.GoogleVoice,
		RuntimeDir: cfg.RuntimeDir,
	}); google != nil {
		registry.register(google)
	}

	return registry
}

func (r *Registry) DefaultBackend() string {
	backend := strings.TrimSpace(r.defaults.DefaultBackend)
	if backend == "" {
		return "windows"
	}
	return strings.ToLower(backend)
}

func (r *Registry) Available() []string {
	names := make([]string, 0, len(r.backends))
	for name := range r.backends {
		names = append(names, name)
	}
	return names
}

func (r *Registry) Speak(ctx context.Context, backend string, request Request) error {
	name := strings.ToLower(strings.TrimSpace(backend))
	if name == "" {
		name = r.DefaultBackend()
	}

	impl, ok := r.backends[name]
	if !ok {
		return fmt.Errorf("tts backend %q is not configured", name)
	}
	if strings.TrimSpace(request.Text) == "" {
		return nil
	}
	if strings.TrimSpace(request.Voice) == "" {
		switch name {
		case "google":
			request.Voice = r.defaults.GoogleVoice
		default:
			request.Voice = r.defaults.WindowsVoice
		}
	}

	return impl.Speak(ctx, request)
}

func (r *Registry) register(backend Backend) {
	if backend == nil {
		return
	}
	r.backends[strings.ToLower(strings.TrimSpace(backend.Name()))] = backend
}
