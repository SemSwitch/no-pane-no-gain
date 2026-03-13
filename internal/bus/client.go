package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

const connectTimeout = 2 * time.Second

type Client struct {
	addr string
	raw  *nats.Conn
	js   nats.JetStreamContext
}

type Message struct {
	Subject    string
	Reply      string
	Data       []byte
	Header     map[string][]string
	ReceivedAt time.Time
}

type Handler func(Message)

type Subscription struct {
	subs []*nats.Subscription
}

func New(addr string) (*Client, error) {
	conn, err := nats.Connect(
		addr,
		nats.Name("nexis"),
		nats.Timeout(connectTimeout),
		nats.RetryOnFailedConnect(false),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats at %s: %w", addr, err)
	}

	js, err := conn.JetStream()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("create jetstream context at %s: %w", addr, err)
	}

	return &Client{
		addr: addr,
		raw:  conn,
		js:   js,
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	flushCtx, cancel := withDefaultTimeout(ctx, connectTimeout)
	defer cancel()
	if err := c.raw.FlushWithContext(flushCtx); err != nil {
		return fmt.Errorf("ping nats at %s: %w", c.addr, err)
	}
	if err := c.raw.LastError(); err != nil {
		return fmt.Errorf("ping nats at %s: %w", c.addr, err)
	}
	return nil
}

func (c *Client) EnsureStream(name string, subjects []string) error {
	if stringsEmpty(name) {
		return errors.New("jetstream stream name must not be empty")
	}
	subjects = mergeSubjects(nil, subjects)
	if len(subjects) == 0 {
		return errors.New("jetstream subjects must not be empty")
	}

	cfg := &nats.StreamConfig{
		Name:     name,
		Subjects: subjects,
		Storage:  nats.FileStorage,
	}

	if info, err := c.js.StreamInfo(name); err == nil && info != nil {
		cfg.Retention = info.Config.Retention
		cfg.MaxMsgs = info.Config.MaxMsgs
		cfg.MaxBytes = info.Config.MaxBytes
		cfg.MaxAge = info.Config.MaxAge
		cfg.Storage = info.Config.Storage
		cfg.Discard = info.Config.Discard
		cfg.Replicas = info.Config.Replicas
		cfg.NoAck = info.Config.NoAck
		_, err = c.js.UpdateStream(cfg)
		if err != nil {
			return fmt.Errorf("update jetstream stream %s: %w", name, err)
		}
		return nil
	} else if err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("lookup jetstream stream %s: %w", name, err)
	}

	if _, err := c.js.AddStream(cfg); err != nil {
		if lookupErr := c.expandCompatibleStream(subjects); lookupErr == nil {
			return nil
		}
		return fmt.Errorf("add jetstream stream %s: %w", name, err)
	}

	return nil
}

func (c *Client) PublishJSON(ctx context.Context, subject string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal event for %s: %w", subject, err)
	}

	return c.PublishBytes(ctx, subject, body)
}

func (c *Client) PublishBytes(ctx context.Context, subject string, body []byte) error {
	if stringsEmpty(subject) {
		return errors.New("publish subject must not be empty")
	}

	if _, err := c.js.Publish(subject, body); err != nil {
		return fmt.Errorf("publish event to %s: %w", subject, err)
	}
	flushCtx, cancel := withDefaultTimeout(ctx, connectTimeout)
	defer cancel()
	if err := c.raw.FlushWithContext(flushCtx); err != nil {
		return fmt.Errorf("flush event to %s: %w", subject, err)
	}

	return nil
}

func (c *Client) Subscribe(subjects []string, handler Handler) (*Subscription, error) {
	if len(subjects) == 0 {
		return nil, errors.New("subscribe subjects must not be empty")
	}
	if handler == nil {
		return nil, errors.New("subscribe handler must not be nil")
	}

	next := &Subscription{subs: make([]*nats.Subscription, 0, len(subjects))}
	for _, subject := range subjects {
		if stringsEmpty(subject) {
			_ = next.Close()
			return nil, errors.New("subscribe subject must not be empty")
		}

		sub, err := c.raw.Subscribe(subject, func(msg *nats.Msg) {
			handler(Message{
				Subject:    msg.Subject,
				Reply:      msg.Reply,
				Data:       append([]byte(nil), msg.Data...),
				Header:     cloneHeader(msg.Header),
				ReceivedAt: time.Now().UTC(),
			})
		})
		if err != nil {
			_ = next.Close()
			return nil, fmt.Errorf("subscribe to %s: %w", subject, err)
		}
		next.subs = append(next.subs, sub)
	}

	if err := c.raw.Flush(); err != nil {
		_ = next.Close()
		return nil, fmt.Errorf("flush subscriptions: %w", err)
	}
	if err := c.raw.LastError(); err != nil {
		_ = next.Close()
		return nil, fmt.Errorf("flush subscriptions: %w", err)
	}

	return next, nil
}

func (c *Client) Close() error {
	c.raw.Close()
	return nil
}

func (s *Subscription) Close() error {
	if s == nil {
		return nil
	}
	var errs []error
	for _, sub := range s.subs {
		if sub == nil {
			continue
		}
		if err := sub.Unsubscribe(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
			errs = append(errs, err)
		}
	}
	s.subs = nil
	return errors.Join(errs...)
}

func stringsEmpty(value string) bool {
	return len(value) == 0
}

func cloneHeader(header nats.Header) map[string][]string {
	if len(header) == 0 {
		return nil
	}

	clone := make(map[string][]string, len(header))
	for key, values := range header {
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		clone[canonical] = append([]string(nil), values...)
	}
	return clone
}

func withDefaultTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), timeout)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func (c *Client) expandCompatibleStream(subjects []string) error {
	for info := range c.js.StreamsInfo() {
		if info == nil {
			continue
		}
		if !streamSubjectsOverlap(info.Config.Subjects, subjects) {
			continue
		}

		nextConfig := info.Config
		nextConfig.Subjects = mergeSubjects(info.Config.Subjects, subjects)
		if _, err := c.js.UpdateStream(&nextConfig); err != nil {
			return fmt.Errorf("update overlapping stream %s: %w", info.Config.Name, err)
		}
		return nil
	}

	return errors.New("no compatible existing stream found")
}

func streamSubjectsOverlap(left []string, right []string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}

	for _, existing := range left {
		for _, required := range right {
			if subjectPatternsOverlap(existing, required) {
				return true
			}
		}
	}

	return false
}

func mergeSubjects(existing []string, required []string) []string {
	merged := make([]string, 0, len(existing)+len(required))
	seen := make(map[string]struct{}, len(existing)+len(required))

	for _, subject := range existing {
		if _, ok := seen[subject]; ok {
			continue
		}
		seen[subject] = struct{}{}
		merged = append(merged, subject)
	}

	for _, subject := range required {
		if _, ok := seen[subject]; ok {
			continue
		}
		seen[subject] = struct{}{}
		merged = append(merged, subject)
	}

	return merged
}

func subjectPatternsOverlap(left string, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return subjectTokensOverlap(strings.Split(left, "."), strings.Split(right, "."))
}

func subjectTokensOverlap(left []string, right []string) bool {
	switch {
	case len(left) == 0 && len(right) == 0:
		return true
	case len(left) == 0:
		return patternMatchesEmptySuffix(right)
	case len(right) == 0:
		return patternMatchesEmptySuffix(left)
	case left[0] == ">":
		return len(left) == 1 && patternCanMatchAny(right)
	case right[0] == ">":
		return len(right) == 1 && patternCanMatchAny(left)
	case left[0] == "*" || right[0] == "*" || left[0] == right[0]:
		return subjectTokensOverlap(left[1:], right[1:])
	default:
		return false
	}
}

func patternCanMatchAny(tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	if tokens[0] == ">" {
		return len(tokens) == 1
	}
	return patternCanMatchAny(tokens[1:])
}

func patternMatchesEmptySuffix(tokens []string) bool {
	return len(tokens) == 0 || (len(tokens) == 1 && tokens[0] == ">")
}
