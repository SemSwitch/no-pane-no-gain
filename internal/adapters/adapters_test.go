package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/SemSwitch/Nexis-Echo/internal/bus"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

type fakeFactory struct{}

func (fakeFactory) Prepare(context.Context, PrepareParams) (Session, error) {
	return NopSession(LaunchSpec{}), nil
}

func TestNewSelectsRegisteredFactory(t *testing.T) {
	t.Parallel()

	want := fakeFactory{}
	got, err := New(
		core.ProviderCodex,
		Named(core.ProviderClaude, fakeFactory{}),
		Named(core.ProviderCodex, want),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("New() factory = %#v, want %#v", got, want)
	}
}

func TestNewReportsSortedAvailableProviders(t *testing.T) {
	t.Parallel()

	_, err := New(
		core.ProviderGemini,
		Named(core.ProviderCodex, fakeFactory{}),
		Named(core.ProviderClaude, fakeFactory{}),
	)
	if err == nil {
		t.Fatal("expected missing adapter to fail")
	}

	message := err.Error()
	if !strings.Contains(message, "no adapter registered for gemini") {
		t.Fatalf("error = %q, want missing adapter prefix", message)
	}
	if !strings.Contains(message, "[claude codex]") {
		t.Fatalf("error = %q, want sorted provider list", message)
	}
}

func TestNopSessionReturnsLaunchSpec(t *testing.T) {
	t.Parallel()

	spec := LaunchSpec{
		ArgsPrefix: []string{"-c", "otel.enabled=true"},
		Env: map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318",
		},
	}

	session := NopSession(spec)
	if got := session.LaunchSpec(); !reflect.DeepEqual(got, spec) {
		t.Fatalf("LaunchSpec() = %#v, want %#v", got, spec)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestPublisherRequiresConfiguredClient(t *testing.T) {
	t.Parallel()

	publisher := NewPublisher(nil)
	err := publisher.Publish(context.Background(), core.Event{Kind: core.EventRegistryOnline, Timestamp: time.Now().UTC()})
	if err == nil || !strings.Contains(err.Error(), "publisher is not configured") {
		t.Fatalf("Publish() error = %v, want configuration failure", err)
	}
}

func TestPublisherPublishAndPublishMany(t *testing.T) {
	url := startTestNATSServer(t)

	client, err := bus.New(url)
	if err != nil {
		t.Fatalf("bus.New() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.EnsureStream("TEST", core.StreamSubjects()); err != nil {
		t.Fatalf("EnsureStream() error = %v", err)
	}

	raw, err := nats.Connect(url, nats.Timeout(time.Second))
	if err != nil {
		t.Fatalf("nats.Connect() error = %v", err)
	}
	defer raw.Close()

	sub, err := raw.SubscribeSync("nexis.>")
	if err != nil {
		t.Fatalf("SubscribeSync() error = %v", err)
	}
	if err := raw.FlushTimeout(time.Second); err != nil {
		t.Fatalf("FlushTimeout() error = %v", err)
	}

	publisher := NewPublisher(client)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	online := core.Event{
		Kind:        core.EventRegistryOnline,
		Provider:    core.ProviderClaude,
		Role:        "builder",
		SessionID:   "builder-claude-1",
		Timestamp:   time.Now().UTC(),
		ContentMode: core.ContentMetadataOnly,
		Source:      core.SourceWrapper,
		Sensitivity: core.SensitivityLocalOnly,
		Metadata: map[string]any{
			"topology_path": `C:\Nexis\.nexis\topology.json`,
		},
	}
	response := core.Event{
		Kind:        core.EventLLMResponse,
		Provider:    core.ProviderCodex,
		Role:        "architect",
		SessionID:   "architect-codex-1",
		RequestID:   "req-123",
		Timestamp:   time.Now().UTC(),
		ContentMode: core.ContentFull,
		Source:      core.SourceGateway,
		Sensitivity: core.SensitivityLocalOnly,
		Payload:     json.RawMessage(`{"response":"ok"}`),
	}

	if err := publisher.Publish(ctx, online); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if err := publisher.PublishMany(ctx, []core.Event{response}); err != nil {
		t.Fatalf("PublishMany() error = %v", err)
	}

	msg1, err := sub.NextMsg(time.Second)
	if err != nil {
		t.Fatalf("NextMsg(online) error = %v", err)
	}
	if msg1.Subject != core.SubjectRegistryOnline {
		t.Fatalf("msg1.Subject = %q, want %q", msg1.Subject, core.SubjectRegistryOnline)
	}

	var gotOnline core.Event
	if err := json.Unmarshal(msg1.Data, &gotOnline); err != nil {
		t.Fatalf("Unmarshal(msg1) error = %v", err)
	}
	if gotOnline.Kind != online.Kind || gotOnline.Role != online.Role || gotOnline.SessionID != online.SessionID {
		t.Fatalf("online event = %#v, want kind=%s role=%s session=%s", gotOnline, online.Kind, online.Role, online.SessionID)
	}

	msg2, err := sub.NextMsg(time.Second)
	if err != nil {
		t.Fatalf("NextMsg(response) error = %v", err)
	}
	if want := core.SubjectLLM + ".codex.architect.response"; msg2.Subject != want {
		t.Fatalf("msg2.Subject = %q, want %q", msg2.Subject, want)
	}

	var gotResponse core.Event
	if err := json.Unmarshal(msg2.Data, &gotResponse); err != nil {
		t.Fatalf("Unmarshal(msg2) error = %v", err)
	}
	if gotResponse.Kind != response.Kind || gotResponse.Provider != response.Provider || gotResponse.RequestID != response.RequestID {
		t.Fatalf("response event = %#v, want kind=%s provider=%s request=%s", gotResponse, response.Kind, response.Provider, response.RequestID)
	}
	if string(gotResponse.Payload) != string(response.Payload) {
		t.Fatalf("response payload = %s, want %s", string(gotResponse.Payload), string(response.Payload))
	}
}

func TestFactoryPrepareSignatureRemainsCompatible(t *testing.T) {
	t.Parallel()

	var factory Factory = fakeFactory{}
	session, err := factory.Prepare(context.Background(), PrepareParams{
		Config: struct{}{},
		Logger: slog.Default(),
		Meta: SessionMeta{
			Role:      "builder",
			Provider:  core.ProviderCodex,
			RepoRoot:  `C:\Nexis`,
			SessionID: "builder-codex-1",
		},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if session == nil {
		t.Fatal("Prepare() returned nil session")
	}
}

func startTestNATSServer(t *testing.T) string {
	t.Helper()

	binary := testNATSServerBinary(t)
	port := reservePort(t)
	dataDir := t.TempDir()
	logFilePath := filepath.Join(dataDir, "nats.log")
	logFile, err := os.Create(logFilePath)
	if err != nil {
		t.Fatalf("os.Create(%q) error = %v", logFilePath, err)
	}

	cmd := exec.Command(binary, "-js", "-a", "127.0.0.1", "-p", strconv.Itoa(port), "-sd", dataDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start nats-server: %v", err)
	}

	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = logFile.Close()
	})

	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := nats.Connect(url, nats.Timeout(200*time.Millisecond), nats.RetryOnFailedConnect(false))
		if err == nil {
			conn.Close()
			return url
		}
		time.Sleep(100 * time.Millisecond)
	}

	body, _ := os.ReadFile(logFilePath)
	t.Fatalf("timed out waiting for nats-server at %s\n%s", url, body)
	return ""
}

func testNATSServerBinary(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	name := "nats-server"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "runtime", "nats", name)
	path = filepath.Clean(path)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("nats-server binary not available at %s: %v", path, err)
	}
	return path
}

func reservePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr type = %T, want *net.TCPAddr", listener.Addr())
	}
	return addr.Port
}
