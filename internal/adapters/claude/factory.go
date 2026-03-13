package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/gateway"
)

const (
	defaultUpstreamURL = "https://api.anthropic.com"
	publishTimeout     = 2 * time.Second
)

type Factory struct {
	upstreamURL string
	newGateway  func(gateway.Config) (*gateway.Gateway, error)
}

type session struct {
	spec     adapters.LaunchSpec
	gateway  *gateway.Gateway
	serveErr chan error
	logger   *slog.Logger
}

func NewFactory() Factory {
	return Factory{
		newGateway: gateway.New,
	}
}

func (f Factory) Prepare(ctx context.Context, params adapters.PrepareParams) (adapters.Session, error) {
	logger := params.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	upstreamURL := f.upstreamURL
	if upstreamURL == "" {
		upstreamURL = resolveUpstreamURL()
	}

	newGateway := f.newGateway
	if newGateway == nil {
		newGateway = gateway.New
	}

	publisher := params.Publisher
	meta := params.Meta

	gw, err := newGateway(gateway.Config{
		UpstreamURL: upstreamURL,
		OnRequest: func(hookCtx context.Context, capture *gateway.RequestCapture) {
			events := normalizeRequest(meta, capture)
			publishEvents(hookCtx, logger, publisher, events)
		},
		OnResponse: func(hookCtx context.Context, exchange *gateway.Exchange) {
			events := normalizeResponse(meta, exchange)
			publishEvents(hookCtx, logger, publisher, events)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("prepare claude gateway: %w", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		err := gw.Start()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	spec := adapters.LaunchSpec{
		Env: map[string]string{
			"ANTHROPIC_BASE_URL": "http://" + gw.Addr(),
			"NO_PROXY":           mergeNoProxy(os.Getenv("NO_PROXY"), "127.0.0.1", "localhost"),
			"no_proxy":           mergeNoProxy(os.Getenv("no_proxy"), "127.0.0.1", "localhost"),
		},
	}

	return &session{
		spec:     spec,
		gateway:  gw,
		serveErr: serveErr,
		logger:   logger,
	}, nil
}

func (s *session) LaunchSpec() adapters.LaunchSpec {
	return s.spec
}

func (s *session) Close(ctx context.Context) error {
	if s.gateway == nil {
		return nil
	}

	stopErr := s.gateway.Stop(ctx)

	select {
	case serveErr := <-s.serveErr:
		if stopErr != nil {
			return stopErr
		}
		return serveErr
	case <-ctx.Done():
		if stopErr != nil {
			return stopErr
		}
		return ctx.Err()
	}
}

func publishEvents(ctx context.Context, logger *slog.Logger, publisher *adapters.Publisher, events []coreEvent) {
	if publisher == nil || len(events) == 0 {
		return
	}

	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()

	typed := make([]core.Event, 0, len(events))
	for _, event := range events {
		typed = append(typed, event.Event)
	}
	if err := publisher.PublishMany(publishCtx, typed); err != nil {
		logger.Warn("publish claude gateway events", "error", err)
	}
}

func resolveUpstreamURL() string {
	raw := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL"))
	if raw == "" {
		return defaultUpstreamURL
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return defaultUpstreamURL
	}
	if isLoopbackHost(parsed.Hostname()) {
		return defaultUpstreamURL
	}
	return strings.TrimRight(raw, "/")
}

func mergeNoProxy(existing string, values ...string) string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values)+1)

	add := func(raw string) {
		for _, item := range strings.Split(raw, ",") {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, trimmed)
		}
	}

	add(existing)
	for _, value := range values {
		add(value)
	}

	return strings.Join(out, ",")
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
