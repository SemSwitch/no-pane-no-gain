package bus

import (
	"context"
	"net"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func TestStreamSubjectsCover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		existing []string
		required []string
		want     bool
	}{
		{
			name:     "exact match",
			existing: []string{"nexis.registry.online", "nexis.registry.offline"},
			required: []string{"nexis.registry.online", "nexis.registry.offline"},
			want:     true,
		},
		{
			name:     "wildcard overlap",
			existing: []string{"nexis.>"},
			required: []string{"nexis.registry.online", "nexis.registry.offline"},
			want:     true,
		},
		{
			name:     "missing overlap",
			existing: []string{"nexis.registry.online"},
			required: []string{"nexis.system.trigger"},
			want:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := streamSubjectsOverlap(tt.existing, tt.required); got != tt.want {
				t.Fatalf("streamSubjectsOverlap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubscribeDeliversLiveMessages(t *testing.T) {
	t.Parallel()

	server := runTestServer(t)
	client, err := New(server.ClientURL())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	received := make(chan Message, 1)
	sub, err := client.Subscribe([]string{"nexis.test.>"}, func(message Message) {
		select {
		case received <- message:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer func() { _ = sub.Close() }()

	raw, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer raw.Close()

	msg := &nats.Msg{
		Subject: "nexis.test.live",
		Data:    []byte(`{"kind":"ok"}`),
		Header:  nats.Header{"X-Nexis-Test": []string{"yes"}},
	}
	if err := raw.PublishMsg(msg); err != nil {
		t.Fatalf("PublishMsg() error = %v", err)
	}
	if err := raw.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	select {
	case got := <-received:
		if got.Subject != "nexis.test.live" {
			t.Fatalf("message subject = %q, want %q", got.Subject, "nexis.test.live")
		}
		if string(got.Data) != `{"kind":"ok"}` {
			t.Fatalf("message data = %q", string(got.Data))
		}
		if got.Header["X-Nexis-Test"][0] != "yes" {
			t.Fatalf("message header = %#v", got.Header)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribed message")
	}
}

func runTestServer(t *testing.T) *natsserver.Server {
	t.Helper()

	port := freePort(t)
	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      port,
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	server, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	go server.Start()
	if !server.ReadyForConnections(5 * time.Second) {
		server.Shutdown()
		t.Fatal("nats test server not ready")
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown()
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	})

	return server
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener addr type %T", listener.Addr())
	}
	if addr.Port == 0 {
		t.Fatal("freePort() allocated zero port")
	}
	return addr.Port
}
