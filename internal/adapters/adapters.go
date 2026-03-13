package adapters

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/SemSwitch/Nexis-Echo/internal/bus"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
)

type SessionMeta struct {
	Role      string
	StableID  string
	Provider  core.Provider
	RepoRoot  string
	SessionID string
}

type Publisher struct {
	client *bus.Client
}

func NewPublisher(client *bus.Client) *Publisher {
	return &Publisher{client: client}
}

func (p *Publisher) Publish(ctx context.Context, event core.Event) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("publisher is not configured")
	}
	return p.client.PublishJSON(ctx, core.SubjectForEvent(event), event)
}

func (p *Publisher) PublishMany(ctx context.Context, events []core.Event) error {
	for _, event := range events {
		if err := p.Publish(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

type LaunchSpec struct {
	ArgsPrefix []string
	Env        map[string]string
}

type Session interface {
	LaunchSpec() LaunchSpec
	Close(context.Context) error
}

type Factory interface {
	Prepare(context.Context, PrepareParams) (Session, error)
}

type PrepareParams struct {
	Config    any
	Logger    *slog.Logger
	Publisher *Publisher
	Meta      SessionMeta
}

type nopSession struct {
	spec LaunchSpec
}

func (s nopSession) LaunchSpec() LaunchSpec {
	return s.spec
}

func (s nopSession) Close(context.Context) error {
	return nil
}

func NopSession(spec LaunchSpec) Session {
	return nopSession{spec: spec}
}

type namedFactory struct {
	provider core.Provider
	factory  Factory
}

func New(provider core.Provider, factories ...namedFactory) (Factory, error) {
	for _, candidate := range factories {
		if candidate.provider == provider && candidate.factory != nil {
			return candidate.factory, nil
		}
	}

	available := make([]string, 0, len(factories))
	for _, candidate := range factories {
		if candidate.provider == "" || candidate.factory == nil {
			continue
		}
		available = append(available, string(candidate.provider))
	}
	sort.Strings(available)
	return nil, fmt.Errorf("no adapter registered for %s (available: %v)", provider, available)
}

func Named(provider core.Provider, factory Factory) namedFactory {
	return namedFactory{provider: provider, factory: factory}
}
