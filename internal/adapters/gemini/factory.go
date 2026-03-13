package gemini

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/telemetry/collector"
)

type collectorServer interface {
	Endpoint() string
	Close(context.Context) error
}

type startCollectorFunc func(collector.Options) (collectorServer, error)
type publishFunc func(context.Context, *adapters.Publisher, []core.Event) error

type factory struct {
	startCollector startCollectorFunc
	publish        publishFunc
}

type session struct {
	collector collectorServer
	spec      adapters.LaunchSpec
}

func NewFactory() adapters.Factory {
	return factory{
		startCollector: func(options collector.Options) (collectorServer, error) {
			return collector.Start(options)
		},
		publish: func(ctx context.Context, publisher *adapters.Publisher, events []core.Event) error {
			if publisher == nil {
				return errors.New("gemini adapter requires publisher")
			}
			return publisher.PublishMany(ctx, events)
		},
	}
}

func (f factory) Prepare(ctx context.Context, params adapters.PrepareParams) (adapters.Session, error) {
	startCollector := f.startCollector
	if startCollector == nil {
		startCollector = func(options collector.Options) (collectorServer, error) {
			return collector.Start(options)
		}
	}

	publish := f.publish
	if publish == nil {
		publish = func(ctx context.Context, publisher *adapters.Publisher, events []core.Event) error {
			if publisher == nil {
				return errors.New("gemini adapter requires publisher")
			}
			return publisher.PublishMany(ctx, events)
		}
	}

	server, err := startCollector(collector.Options{
		Logger: params.Logger,
		Handler: func(callbackCtx context.Context, request collector.Request) error {
			return handlePayload(callbackCtx, publish, params, request)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("start gemini telemetry collector: %w", err)
	}

	return &session{
		collector: server,
		spec: adapters.LaunchSpec{
			Env: buildLaunchEnv(server.Endpoint(), params.Meta.Role),
		},
	}, nil
}

func (s *session) LaunchSpec() adapters.LaunchSpec {
	if s == nil {
		return adapters.LaunchSpec{}
	}
	return s.spec
}

func (s *session) Close(ctx context.Context) error {
	if s == nil || s.collector == nil {
		return nil
	}

	closeCtx := ctx
	if closeCtx == nil {
		var cancel context.CancelFunc
		closeCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}

	return s.collector.Close(closeCtx)
}

func buildLaunchEnv(endpoint string, role string) map[string]string {
	env := map[string]string{
		"GEMINI_TELEMETRY_ENABLED":       "1",
		"GEMINI_TELEMETRY_TARGET":        "local",
		"GEMINI_TELEMETRY_LOG_PROMPTS":   "1",
		"GEMINI_TELEMETRY_USE_COLLECTOR": "1",
		"GEMINI_TELEMETRY_OTLP_ENDPOINT": endpoint,
		"GEMINI_TELEMETRY_OTLP_PROTOCOL": "http",
		"OTEL_EXPORTER_OTLP_ENDPOINT":    endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL":    "http/json",
		"OTEL_LOGS_EXPORTER":             "otlp",
		"OTEL_TRACES_EXPORTER":           "otlp",
		"NO_PROXY":                       mergeNoProxy(os.Getenv("NO_PROXY"), "127.0.0.1", "localhost"),
		"no_proxy":                       mergeNoProxy(os.Getenv("no_proxy"), "127.0.0.1", "localhost"),
	}

	if trimmedRole := strings.TrimSpace(role); trimmedRole != "" {
		env["OTEL_RESOURCE_ATTRIBUTES"] = appendResourceAttribute(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), "instance.role="+trimmedRole)
	}

	return env
}

func handlePayload(ctx context.Context, publish publishFunc, params adapters.PrepareParams, request collector.Request) error {
	events, err := DecodeAndNormalize(request.Body, Context{
		RoleOverride: params.Meta.Role,
		RepoRoot:     params.Meta.RepoRoot,
	})
	if err != nil {
		return fmt.Errorf("normalize gemini otlp payload: %w", err)
	}
	if len(events) == 0 {
		return nil
	}

	for index := range events {
		events[index] = enrichEvent(events[index], params, request)
	}

	return publish(ctx, params.Publisher, events)
}

func enrichEvent(event core.Event, params adapters.PrepareParams, request collector.Request) core.Event {
	event.Provider = core.ProviderGemini
	if event.Role == "" {
		event.Role = params.Meta.Role
	}
	if event.StableID == "" {
		event.StableID = params.Meta.StableID
	}
	if event.RepoRoot == "" {
		event.RepoRoot = params.Meta.RepoRoot
	}
	if event.Metadata == nil {
		event.Metadata = map[string]any{}
	}
	event.Metadata["attach_session_id"] = params.Meta.SessionID
	event.Metadata["collector_path"] = request.Path
	if params.Meta.StableID != "" {
		event.Metadata["stable_id"] = params.Meta.StableID
	}
	return event
}

func appendResourceAttribute(existing string, attribute string) string {
	existing = strings.TrimSpace(existing)
	attribute = strings.TrimSpace(attribute)
	switch {
	case existing == "":
		return attribute
	case attribute == "":
		return existing
	default:
		return existing + "," + attribute
	}
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
