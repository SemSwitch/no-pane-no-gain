package gemini

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/telemetry/collector"
)

func TestFactoryPrepare_SetsGeminiTelemetryEnv(t *testing.T) {
	session, err := factory{
		startCollector: func(options collector.Options) (collectorServer, error) {
			return collector.Start(options)
		},
		publish: func(context.Context, *adapters.Publisher, []core.Event) error {
			return nil
		},
	}.Prepare(context.Background(), adapters.PrepareParams{
		Publisher: &adapters.Publisher{},
		Meta: adapters.SessionMeta{
			Role: "Architect",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer func() {
		_ = session.Close(context.Background())
	}()

	spec := session.LaunchSpec()
	if spec.Env["GEMINI_TELEMETRY_ENABLED"] != "1" {
		t.Fatalf("expected GEMINI_TELEMETRY_ENABLED=1, got %q", spec.Env["GEMINI_TELEMETRY_ENABLED"])
	}
	if spec.Env["GEMINI_TELEMETRY_USE_COLLECTOR"] != "1" {
		t.Fatalf("expected GEMINI_TELEMETRY_USE_COLLECTOR=1, got %q", spec.Env["GEMINI_TELEMETRY_USE_COLLECTOR"])
	}
	if spec.Env["GEMINI_TELEMETRY_TARGET"] != "local" {
		t.Fatalf("expected GEMINI_TELEMETRY_TARGET=local, got %q", spec.Env["GEMINI_TELEMETRY_TARGET"])
	}
	if spec.Env["GEMINI_TELEMETRY_OTLP_PROTOCOL"] != "http" {
		t.Fatalf("expected GEMINI_TELEMETRY_OTLP_PROTOCOL=http, got %q", spec.Env["GEMINI_TELEMETRY_OTLP_PROTOCOL"])
	}
	if spec.Env["GEMINI_TELEMETRY_OTLP_ENDPOINT"] == "" {
		t.Fatal("expected GEMINI_TELEMETRY_OTLP_ENDPOINT to be set")
	}
	if spec.Env["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/json" {
		t.Fatalf("expected OTEL_EXPORTER_OTLP_PROTOCOL=http/json, got %q", spec.Env["OTEL_EXPORTER_OTLP_PROTOCOL"])
	}
	if spec.Env["OTEL_RESOURCE_ATTRIBUTES"] != "instance.role=Architect" {
		t.Fatalf("expected OTEL_RESOURCE_ATTRIBUTES to include role, got %q", spec.Env["OTEL_RESOURCE_ATTRIBUTES"])
	}
	if spec.Env["OTEL_EXPORTER_OTLP_ENDPOINT"] == "" {
		t.Fatal("expected OTEL_EXPORTER_OTLP_ENDPOINT to be set")
	}
}

func TestFactoryPrepare_CollectorPublishesNormalizedEvents(t *testing.T) {
	logBody := mustReadRuntimeFixture(t, "testdata/logs.json")
	spanBody := mustReadRuntimeFixture(t, "testdata/spans.json")

	published := make(chan []core.Event, 2)
	session, err := factory{
		startCollector: func(options collector.Options) (collectorServer, error) {
			return collector.Start(options)
		},
		publish: func(_ context.Context, _ *adapters.Publisher, events []core.Event) error {
			copied := append([]core.Event(nil), events...)
			published <- copied
			return nil
		},
	}.Prepare(context.Background(), adapters.PrepareParams{
		Publisher: &adapters.Publisher{},
		Meta: adapters.SessionMeta{
			Role:      "Builder",
			StableID:  "st_builder",
			RepoRoot:  `C:\repo`,
			SessionID: "attach-session-123",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer func() {
		_ = session.Close(context.Background())
	}()

	endpoint := session.LaunchSpec().Env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	postOTLP(t, endpoint, logBody)
	postOTLP(t, endpoint+"/v1/traces", spanBody)

	first := waitForEvents(t, published)
	second := waitForEvents(t, published)
	events := append(first, second...)

	if len(events) != 8 {
		t.Fatalf("expected 8 normalized events, got %d", len(events))
	}

	for _, event := range events {
		if event.Provider != core.ProviderGemini {
			t.Fatalf("expected provider gemini, got %q", event.Provider)
		}
		if event.Role != "Builder" {
			t.Fatalf("expected role Builder, got %q", event.Role)
		}
		if event.RepoRoot != `C:\repo` {
			t.Fatalf("expected repo root to be propagated, got %q", event.RepoRoot)
		}
		if got := event.Metadata["attach_session_id"]; got != "attach-session-123" {
			t.Fatalf("expected attach_session_id metadata, got %#v", got)
		}
		if event.StableID != "st_builder" {
			t.Fatalf("expected stable id st_builder, got %q", event.StableID)
		}
	}
}

func TestSessionClose_ShutsCollectorDown(t *testing.T) {
	session, err := factory{
		startCollector: func(options collector.Options) (collectorServer, error) {
			return collector.Start(options)
		},
		publish: func(context.Context, *adapters.Publisher, []core.Event) error {
			return nil
		},
	}.Prepare(context.Background(), adapters.PrepareParams{
		Publisher: &adapters.Publisher{},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	endpoint := session.LaunchSpec().Env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := session.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}
	_, err = client.Post(endpoint+"/v1/logs", "application/json", bytes.NewReader([]byte(`{}`)))
	if err == nil {
		t.Fatal("expected collector POST to fail after Close")
	}
}

func postOTLP(t *testing.T, endpoint string, body []byte) {
	t.Helper()

	response, err := http.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 response from %s, got %d", endpoint, response.StatusCode)
	}
}

func waitForEvents(t *testing.T, published <-chan []core.Event) []core.Event {
	t.Helper()

	select {
	case events := <-published:
		return events
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for normalized events")
		return nil
	}
}

func mustReadRuntimeFixture(t *testing.T, relative string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(relative))
	if err != nil {
		t.Fatalf("read fixture %s: %v", relative, err)
	}
	return body
}
