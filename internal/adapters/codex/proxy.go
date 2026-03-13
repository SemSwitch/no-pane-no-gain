package codex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
)

type httpRequestCapture struct {
	RequestID  string
	Method     string
	URL        string
	Path       string
	Header     http.Header
	Body       []byte
	ReceivedAt time.Time
}

type httpResponseCapture struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Duration   time.Duration
	ReceivedAt time.Time
}

type httpExchange struct {
	RequestID string
	Request   *httpRequestCapture
	Response  *httpResponseCapture
}

type wsMessageCapture struct {
	RequestID   string
	Path        string
	Header      http.Header
	MessageType int
	Body        []byte
	ReceivedAt  time.Time
}

type wsErrorCapture struct {
	RequestID  string
	Path       string
	Err        error
	ReceivedAt time.Time
}

type proxyConfig struct {
	ListenPort        int
	HTTPBaseURL       string
	WSBaseURL         string
	HTTPResponsesURL  string
	WSResponsesURL    string
	OnHTTPRequest     func(context.Context, *httpRequestCapture)
	OnHTTPResponse    func(context.Context, *httpExchange)
	OnWSClientMessage func(context.Context, *wsMessageCapture)
	OnWSServerMessage func(context.Context, *wsMessageCapture)
	OnWSError         func(context.Context, *wsErrorCapture)
	Logger            *slog.Logger
	Publisher         *adapters.Publisher
	Meta              adapters.SessionMeta
}

type proxy struct {
	cfg      proxyConfig
	server   *http.Server
	listener net.Listener
	client   *http.Client
}

func newProxy(cfg proxyConfig) (*proxy, error) {
	if strings.TrimSpace(cfg.HTTPBaseURL) == "" {
		return nil, fmt.Errorf("codex proxy: HTTPBaseURL is required")
	}
	if strings.TrimSpace(cfg.WSBaseURL) == "" {
		return nil, fmt.Errorf("codex proxy: WSBaseURL is required")
	}
	if strings.TrimSpace(cfg.HTTPResponsesURL) == "" {
		return nil, fmt.Errorf("codex proxy: HTTPResponsesURL is required")
	}
	if strings.TrimSpace(cfg.WSResponsesURL) == "" {
		return nil, fmt.Errorf("codex proxy: WSResponsesURL is required")
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.ListenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("codex proxy listen %s: %w", addr, err)
	}

	p := &proxy{
		cfg:      cfg,
		listener: ln,
		client:   http.DefaultClient,
	}
	p.server = &http.Server{Handler: http.HandlerFunc(p.handle)}
	return p, nil
}

func (p *proxy) Addr() string {
	return p.listener.Addr().String()
}

func (p *proxy) Start() error {
	return p.server.Serve(p.listener)
}

func (p *proxy) Stop(ctx context.Context) error {
	return p.server.Shutdown(ctx)
}

func (p *proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/responses" && websocket.IsWebSocketUpgrade(r) {
		p.handleResponsesWebSocket(w, r)
		return
	}
	p.handleHTTP(w, r)
}

func (p *proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now()
	requestID := generateID()

	var reqBody []byte
	if r.Body != nil {
		var err error
		reqBody, err = io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "codex proxy: failed to read request body", http.StatusBadGateway)
			return
		}
	}

	capture := &httpRequestCapture{
		RequestID:  requestID,
		Method:     r.Method,
		URL:        r.URL.String(),
		Path:       r.URL.Path,
		Header:     r.Header.Clone(),
		Body:       reqBody,
		ReceivedAt: receivedAt,
	}
	if p.cfg.OnHTTPRequest != nil {
		p.cfg.OnHTTPRequest(r.Context(), capture)
	}

	upstreamURL := p.resolveHTTPUpstreamURL(r)
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "codex proxy: failed to create upstream request", http.StatusBadGateway)
		return
	}
	copyHTTPHeaders(upReq.Header, r.Header)

	resp, err := p.client.Do(upReq)
	if err != nil {
		http.Error(w, "codex proxy: upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "codex proxy: failed to read upstream response", http.StatusBadGateway)
		return
	}

	if p.cfg.OnHTTPResponse != nil {
		p.cfg.OnHTTPResponse(r.Context(), &httpExchange{
			RequestID: requestID,
			Request:   capture,
			Response: &httpResponseCapture{
				StatusCode: resp.StatusCode,
				Header:     resp.Header.Clone(),
				Body:       respBody,
				Duration:   time.Since(receivedAt),
				ReceivedAt: time.Now().UTC(),
			},
		})
	}

	writeHTTPResponse(w, resp.StatusCode, resp.Header, respBody)
}

func (p *proxy) handleResponsesWebSocket(w http.ResponseWriter, r *http.Request) {
	requestID := generateID()
	path := r.URL.Path

	upstreamURL := p.resolveWSUpstreamURL(r)
	upstreamHeader := filterWSForwardHeaders(r.Header)
	dialer := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  30 * time.Second,
		EnableCompression: true,
		Subprotocols:      websocket.Subprotocols(r),
	}

	upstreamConn, resp, err := dialer.DialContext(r.Context(), upstreamURL, upstreamHeader)
	if err != nil {
		p.emitWSError(r.Context(), requestID, path, err)
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			writeHTTPResponse(w, resp.StatusCode, resp.Header, body)
			return
		}
		http.Error(w, "codex proxy: websocket upstream dial failed", http.StatusBadGateway)
		return
	}
	defer upstreamConn.Close()

	upgrader := websocket.Upgrader{
		CheckOrigin:       func(*http.Request) bool { return true },
		EnableCompression: true,
	}
	if subprotocol := upstreamConn.Subprotocol(); subprotocol != "" {
		upgrader.Subprotocols = []string{subprotocol}
	}

	downstreamConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		p.emitWSError(r.Context(), requestID, path, err)
		return
	}
	defer downstreamConn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	errCh := make(chan error, 2)
	var once sync.Once
	closeBoth := func() {
		_ = downstreamConn.Close()
		_ = upstreamConn.Close()
	}

	go func() {
		errCh <- p.pipeWebSocket(ctx, requestID, path, r.Header.Clone(), downstreamConn, upstreamConn, p.cfg.OnWSClientMessage)
	}()
	go func() {
		errCh <- p.pipeWebSocket(ctx, requestID, path, cloneHeader(resp.Header), upstreamConn, downstreamConn, p.cfg.OnWSServerMessage)
	}()

	err = <-errCh
	cancel()
	once.Do(closeBoth)
	if err != nil && !isNormalClose(err) {
		p.emitWSError(r.Context(), requestID, path, err)
	}
	<-errCh
}

func (p *proxy) pipeWebSocket(ctx context.Context, requestID string, path string, header http.Header, src *websocket.Conn, dst *websocket.Conn, hook func(context.Context, *wsMessageCapture)) error {
	for {
		messageType, body, err := src.ReadMessage()
		if err != nil {
			return err
		}

		if hook != nil && (messageType == websocket.TextMessage || messageType == websocket.BinaryMessage) {
			hook(ctx, &wsMessageCapture{
				RequestID:   requestID,
				Path:        path,
				Header:      cloneHeader(header),
				MessageType: messageType,
				Body:        append([]byte(nil), body...),
				ReceivedAt:  time.Now().UTC(),
			})
		}

		if err := dst.WriteMessage(messageType, body); err != nil {
			return err
		}
	}
}

func (p *proxy) emitWSError(ctx context.Context, requestID string, path string, err error) {
	if p.cfg.OnWSError == nil || err == nil {
		return
	}
	p.cfg.OnWSError(ctx, &wsErrorCapture{
		RequestID:  requestID,
		Path:       path,
		Err:        err,
		ReceivedAt: time.Now().UTC(),
	})
}

func (p *proxy) resolveHTTPUpstreamURL(r *http.Request) string {
	if r.URL.Path == "/v1/responses" {
		return appendRawQuery(strings.TrimSpace(p.cfg.HTTPResponsesURL), r.URL.RawQuery)
	}
	return strings.TrimRight(p.cfg.HTTPBaseURL, "/") + r.URL.RequestURI()
}

func (p *proxy) resolveWSUpstreamURL(r *http.Request) string {
	if r.URL.Path == "/v1/responses" {
		return appendRawQuery(strings.TrimSpace(p.cfg.WSResponsesURL), r.URL.RawQuery)
	}
	return strings.TrimRight(p.cfg.WSBaseURL, "/") + r.URL.RequestURI()
}

func copyHTTPHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cloneHeader(src http.Header) http.Header {
	if src == nil {
		return http.Header{}
	}
	return src.Clone()
}

func filterWSForwardHeaders(src http.Header) http.Header {
	dst := http.Header{}
	for key, values := range src {
		switch strings.ToLower(key) {
		case "connection", "upgrade", "sec-websocket-key", "sec-websocket-version", "sec-websocket-extensions", "sec-websocket-protocol", "host":
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	return dst
}

func writeHTTPResponse(w http.ResponseWriter, status int, header http.Header, body []byte) {
	for key, values := range header {
		switch strings.ToLower(key) {
		case "content-length", "transfer-encoding", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailers", "upgrade":
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func isNormalClose(err error) bool {
	if err == nil {
		return true
	}
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "use of closed network connection")
}

func generateID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("gw-%d", time.Now().UnixNano())
	}
	return "gw-" + hex.EncodeToString(buf[:])
}

func appendRawQuery(base string, rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return base
	}
	if strings.Contains(base, "?") {
		return base + "&" + rawQuery
	}
	return base + "?" + rawQuery
}
