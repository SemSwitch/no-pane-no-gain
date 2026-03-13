package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

const (
	defaultHTTPUpstreamURL          = "https://api.openai.com"
	defaultWSUpstreamURL            = "wss://api.openai.com"
	defaultHTTPResponsesUpstreamURL = "https://chatgpt.com/backend-api/codex/responses"
	defaultWSResponsesUpstreamURL   = "wss://chatgpt.com/backend-api/codex/responses"
	closeTimeout                    = 2 * time.Second
)

type Factory struct {
	HTTPUpstreamURL          string
	WSUpstreamURL            string
	HTTPResponsesUpstreamURL string
	WSResponsesUpstreamURL   string
}

func NewFactory() Factory {
	return Factory{}
}

func (f Factory) Prepare(ctx context.Context, params adapters.PrepareParams) (adapters.Session, error) {
	logger := params.Logger
	if logger == nil {
		logger = slog.Default()
	}

	session := &session{
		logger:    logger.With("provider", "codex", "role", params.Meta.Role),
		publisher: params.Publisher,
		meta:      params.Meta,
		done:      make(chan struct{}),
		wsStates:  make(map[string]*responseStreamState),
	}

	gw, err := newProxy(proxyConfig{
		ListenPort:       0,
		HTTPBaseURL:      f.httpUpstreamURL(),
		WSBaseURL:        f.wsUpstreamURL(),
		HTTPResponsesURL: f.httpResponsesUpstreamURL(),
		WSResponsesURL:   f.wsResponsesUpstreamURL(),
		OnHTTPRequest: func(hookCtx context.Context, req *httpRequestCapture) {
			session.handleRequest(hookCtx, req)
		},
		OnHTTPResponse: func(hookCtx context.Context, exchange *httpExchange) {
			session.handleResponse(hookCtx, exchange)
		},
		OnWSClientMessage: func(hookCtx context.Context, message *wsMessageCapture) {
			session.handleWSClientMessage(hookCtx, message)
		},
		OnWSServerMessage: func(hookCtx context.Context, message *wsMessageCapture) {
			session.handleWSServerMessage(hookCtx, message)
		},
		OnWSError: func(hookCtx context.Context, capture *wsErrorCapture) {
			session.handleWSError(hookCtx, capture)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("prepare codex gateway: %w", err)
	}

	session.gateway = gw
	session.launch = adapters.LaunchSpec{
		Env: map[string]string{
			"OPENAI_BASE_URL": gatewayBaseURL(gw.Addr()),
			"NO_PROXY":        mergeNoProxy(os.Getenv("NO_PROXY"), "127.0.0.1", "localhost"),
			"no_proxy":        mergeNoProxy(os.Getenv("no_proxy"), "127.0.0.1", "localhost"),
		},
	}

	go session.serve()
	return session, nil
}

func (f Factory) httpUpstreamURL() string {
	if strings.TrimSpace(f.HTTPUpstreamURL) != "" {
		return strings.TrimSpace(f.HTTPUpstreamURL)
	}
	return defaultHTTPUpstreamURL
}

func (f Factory) wsUpstreamURL() string {
	if strings.TrimSpace(f.WSUpstreamURL) != "" {
		return strings.TrimSpace(f.WSUpstreamURL)
	}
	return defaultWSUpstreamURL
}

func (f Factory) httpResponsesUpstreamURL() string {
	if strings.TrimSpace(f.HTTPResponsesUpstreamURL) != "" {
		return strings.TrimSpace(f.HTTPResponsesUpstreamURL)
	}
	return defaultHTTPResponsesUpstreamURL
}

func (f Factory) wsResponsesUpstreamURL() string {
	if strings.TrimSpace(f.WSResponsesUpstreamURL) != "" {
		return strings.TrimSpace(f.WSResponsesUpstreamURL)
	}
	return defaultWSResponsesUpstreamURL
}

type session struct {
	logger    *slog.Logger
	publisher *adapters.Publisher
	meta      adapters.SessionMeta
	gateway   *proxy
	launch    adapters.LaunchSpec

	closeOnce sync.Once
	done      chan struct{}
	wsMu      sync.Mutex
	wsStates  map[string]*responseStreamState
}

func (s *session) LaunchSpec() adapters.LaunchSpec {
	return s.launch
}

func (s *session) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, closeTimeout)
		defer cancel()
	}

	var closeErr error
	s.closeOnce.Do(func() {
		if s.gateway != nil {
			closeErr = s.gateway.Stop(ctx)
		}
	})

	select {
	case <-s.done:
	case <-ctx.Done():
		if closeErr == nil {
			closeErr = ctx.Err()
		}
	}

	return closeErr
}

func (s *session) serve() {
	defer close(s.done)

	if s.gateway == nil {
		return
	}

	if err := s.gateway.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.logger.Warn("codex gateway stopped unexpectedly", "error", err)
	}
}

func (s *session) handleRequest(ctx context.Context, req *httpRequestCapture) {
	if req == nil || req.Path != "/v1/responses" {
		return
	}
	events, err := RequestEvents(s.meta, req)
	if err != nil {
		s.logger.Warn("normalize codex request", "request_id", req.RequestID, "error", err)
		return
	}
	s.publish(ctx, events)
}

func (s *session) handleResponse(ctx context.Context, exchange *httpExchange) {
	if exchange == nil || exchange.Request == nil || exchange.Request.Path != "/v1/responses" {
		return
	}
	events, err := ResponseEvents(s.meta, exchange)
	if err != nil {
		s.logger.Warn("normalize codex response", "request_id", exchange.RequestID, "error", err)
		return
	}
	s.publish(ctx, events)
}

func (s *session) handleWSClientMessage(ctx context.Context, message *wsMessageCapture) {
	events, err := RequestMessageEvents(s.meta, message)
	if err != nil {
		s.logger.Warn("normalize codex websocket request", "request_id", message.RequestID, "error", err)
		return
	}
	s.publish(ctx, events)
}

func (s *session) handleWSServerMessage(ctx context.Context, message *wsMessageCapture) {
	state := s.responseState(message.RequestID)
	events, err := ResponseMessageEvents(s.meta, state, message)
	if err != nil {
		s.logger.Warn("normalize codex websocket response", "request_id", message.RequestID, "error", err)
		return
	}
	s.publish(ctx, events)
}

func (s *session) handleWSError(ctx context.Context, capture *wsErrorCapture) {
	if capture == nil {
		return
	}
	if state := s.responseState(capture.RequestID); state.Completed() {
		s.logger.Debug("ignoring post-completion codex websocket error", "request_id", capture.RequestID, "path", capture.Path, "error", capture.Err)
		return
	}
	event, err := wsErrorEvent(s.meta, capture)
	if err != nil {
		s.logger.Warn("build codex websocket error event", "request_id", capture.RequestID, "error", err)
		return
	}
	s.publish(ctx, []core.Event{event})
}

func (s *session) responseState(requestID string) *responseStreamState {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()

	state, ok := s.wsStates[requestID]
	if ok {
		return state
	}
	state = &responseStreamState{}
	s.wsStates[requestID] = state
	return state
}

func (s *session) publish(ctx context.Context, events []core.Event) {
	if len(events) == 0 || s.publisher == nil {
		return
	}

	if err := s.publisher.PublishMany(ctx, events); err != nil {
		s.logger.Warn("publish codex events", "count", len(events), "error", err)
	}
}

func gatewayBaseURL(addr string) string {
	return "http://" + strings.TrimSpace(addr) + "/v1"
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

func wsErrorEvent(meta adapters.SessionMeta, capture *wsErrorCapture) (core.Event, error) {
	if capture == nil {
		return core.Event{}, fmt.Errorf("codex websocket error capture is nil")
	}

	return newEventWithMode(
		meta,
		core.EventAgentError,
		capture.RequestID,
		capture.ReceivedAt,
		core.ContentMetadataOnly,
		map[string]any{
			"http": map[string]any{
				"path": capture.Path,
			},
			"error": capture.Err.Error(),
		},
		map[string]any{
			"http_path": capture.Path,
		},
	), nil
}
