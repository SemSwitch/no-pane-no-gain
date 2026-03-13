package localnats

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/bus"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	nserver "github.com/nats-io/nats-server/v2/server"
)

const readyTimeout = 10 * time.Second

type Server struct {
	raw    *nserver.Server
	client *bus.Client
	logger *slog.Logger
}

func Start(storeDir string, logger *slog.Logger) (*Server, error) {
	if storeDir == "" {
		return nil, fmt.Errorf("embedded nats store directory must not be empty")
	}
	if err := os.MkdirAll(filepath.Clean(storeDir), 0o755); err != nil {
		return nil, fmt.Errorf("create embedded nats store directory: %w", err)
	}

	opts := &nserver.Options{
		ServerName: "nexis-echo",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   storeDir,
		NoLog:      true,
		NoSigs:     true,
	}

	raw, err := nserver.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("create embedded nats server: %w", err)
	}

	go raw.Start()
	if !raw.ReadyForConnections(readyTimeout) {
		raw.Shutdown()
		raw.WaitForShutdown()
		return nil, fmt.Errorf("embedded nats server did not become ready within %s", readyTimeout)
	}

	client, err := bus.New(raw.ClientURL())
	if err != nil {
		raw.Shutdown()
		raw.WaitForShutdown()
		return nil, fmt.Errorf("connect embedded nats client: %w", err)
	}
	if err := client.EnsureStream("NEXIS_ECHO", core.StreamSubjects()); err != nil {
		_ = client.Close()
		raw.Shutdown()
		raw.WaitForShutdown()
		return nil, fmt.Errorf("ensure embedded event stream: %w", err)
	}

	if logger != nil {
		logger.Info("embedded nats started", "url", raw.ClientURL())
	}

	return &Server{raw: raw, client: client, logger: logger}, nil
}

func (s *Server) URL() string {
	if s == nil || s.raw == nil {
		return ""
	}
	return s.raw.ClientURL()
}

func (s *Server) Client() *bus.Client {
	if s == nil {
		return nil
	}
	return s.client
}

func (s *Server) Stop(ctx context.Context) error {
	return s.Close(ctx)
}

func (s *Server) Close(context.Context) error {
	if s == nil || s.raw == nil {
		return nil
	}
	if s.logger != nil {
		s.logger.Info("stopping embedded nats", "url", s.raw.ClientURL())
	}
	if s.client != nil {
		if err := s.client.Close(); err != nil {
			return err
		}
	}
	s.raw.Shutdown()
	s.raw.WaitForShutdown()
	return nil
}
