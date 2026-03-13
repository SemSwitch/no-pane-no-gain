package echo

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SemSwitch/Nexis-Echo/internal/adapters"
	claudeadapter "github.com/SemSwitch/Nexis-Echo/internal/adapters/claude"
	codexadapter "github.com/SemSwitch/Nexis-Echo/internal/adapters/codex"
	geminiadapter "github.com/SemSwitch/Nexis-Echo/internal/adapters/gemini"
	"github.com/SemSwitch/Nexis-Echo/internal/bus"
	"github.com/SemSwitch/Nexis-Echo/internal/config"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/home"
	"github.com/SemSwitch/Nexis-Echo/internal/localnats"
	"github.com/SemSwitch/Nexis-Echo/internal/repo"
	"github.com/SemSwitch/Nexis-Echo/internal/speech"
	"github.com/SemSwitch/Nexis-Echo/internal/store"
	"github.com/SemSwitch/Nexis-Echo/internal/tts"
)

type Runner struct {
	cfg    config.Config
	logger *slog.Logger
}

func NewRunner(cfg config.Config, logger *slog.Logger) *Runner {
	return &Runner{cfg: cfg, logger: ensureLogger(logger)}
}

func Run(ctx context.Context, options RunOptions) error {
	cfg := config.Default()
	runner := NewRunner(cfg, nil)
	return runner.RunCapture(ctx, options)
}

func (r *Runner) RunCapture(ctx context.Context, options RunOptions) error {
	if err := options.normalize(); err != nil {
		return err
	}

	paths, err := home.Resolve(r.cfg.App.HomeDir)
	if err != nil {
		return err
	}
	if options.Paths.Root != "" {
		paths = options.Paths
	}
	options.Paths = paths
	if err := options.Paths.Ensure(); err != nil {
		return err
	}
	if options.Mode == "" {
		options.Mode = r.cfg.Speech.DefaultMode
	}
	if options.Backend == "" {
		options.Backend = r.cfg.TTS.DefaultBackend
	}
	if options.WindowsVoice == "" {
		options.WindowsVoice = firstNonEmpty(r.cfg.TTS.WindowsVoice, r.cfg.TTS.Voice)
	}
	if options.GoogleVoice == "" {
		options.GoogleVoice = r.cfg.TTS.GoogleVoice
	}
	if options.GoogleAPIKey == "" {
		options.GoogleAPIKey = firstNonEmpty(r.cfg.TTS.GoogleAPIKey, os.Getenv("NEXIS_ECHO_TTS_GOOGLE_API_KEY"), os.Getenv("NEXIS_ECHO_GOOGLE_TTS_API_KEY"))
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = r.cfg.TTS.LeaseTTL.Std()
	}
	if options.SpeakerPollInterval <= 0 {
		options.SpeakerPollInterval = r.cfg.TTS.PollInterval.Std()
	}
	if options.CompletionGrace <= 0 {
		options.CompletionGrace = r.cfg.Pipeline.ReconcileTimeout.Std()
	}

	repoInfo, err := repo.Detect(firstNonEmpty(options.RepoRoot, mustGetwd()))
	if err != nil {
		return err
	}

	provider, err := core.ParseProvider(string(options.Provider))
	if err != nil {
		return err
	}

	providerCommand := strings.TrimSpace(options.ProviderCommandPath)
	if providerCommand == "" {
		providerCommand, err = resolveProviderCommand(provider)
		if err != nil {
			return err
		}
	}

	database, err := store.Open(options.Paths.DB)
	if err != nil {
		return err
	}
	defer database.Close()

	if err := database.ResetInFlightJobs(ctx); err != nil {
		return err
	}

	embeddedBus, err := localnats.Start(filepath.Join(options.Paths.Runtime, "nats"), r.logger)
	if err != nil {
		return err
	}
	defer embeddedBus.Close(context.Background())

	captureID := NewCaptureID(repoInfo.Root, options.CaptureName)
	now := time.Now().UTC()
	commandJSON := marshalCommand(append([]string{providerCommand}, options.ProviderArgs...))
	if err := database.UpsertCapture(ctx, store.Capture{
		ID:          captureID.Key,
		RepoRoot:    repoInfo.Root,
		RepoHash:    repoInfo.Hash,
		Name:        options.CaptureName,
		Provider:    string(provider),
		Mode:        options.Mode,
		Status:      "running",
		CommandJSON: commandJSON,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		return err
	}

	registry := tts.NewRegistry(tts.Config{
		DefaultBackend: options.Backend,
		RuntimeDir:     options.Paths.Runtime,
		WindowsVoice:   options.WindowsVoice,
		GoogleAPIKey:   options.GoogleAPIKey,
		GoogleVoice:    options.GoogleVoice,
	}, r.logger)
	worker := tts.NewWorker(database, r.logger, registry, options.Backend, options.SpeakerPollInterval, options.LeaseTTL)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	go func() {
		if err := worker.Run(workerCtx); err != nil {
			r.logger.Warn("tts worker stopped", "error", err)
		}
	}()

	proc := &processor{
		store:       database,
		logger:      r.logger,
		captureID:   captureID.Key,
		captureName: captureID.Name,
		mode:        options.Mode,
		backend:     options.Backend,
		summarizer: speech.NewSummarizer(speech.Config{
			Provider:        r.cfg.Summarizer.Provider,
			ConciseMaxChars: r.cfg.Speech.ConciseMaxChars,
			NormalMaxChars:  r.cfg.Speech.NormalMaxChars,
		}),
		completionGrace: options.CompletionGrace,
	}

	subscription, err := embeddedBus.Client().Subscribe(core.StreamSubjects(), func(message bus.Message) {
		if err := proc.HandleEvent(context.Background(), message.Data); err != nil {
			r.logger.Warn("handle capture event failed", "subject", message.Subject, "error", err)
		}
	})
	if err != nil {
		return err
	}
	defer subscription.Close()

	session, err := prepareSession(ctx, r.cfg, r.logger, embeddedBus.Client(), captureID, provider, repoInfo.Root)
	if err != nil {
		_ = database.UpdateCaptureState(ctx, captureID.Key, "error", time.Now().UTC())
		return err
	}
	defer session.Close(context.Background())

	cmd := exec.CommandContext(ctx, providerCommand, append(session.LaunchSpec().ArgsPrefix, options.ProviderArgs...)...)
	cmd.Dir = repoInfo.Root
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = mergeEnv(os.Environ(), session.LaunchSpec().Env)

	r.logger.Info("starting capture", "capture", options.CaptureName, "provider", provider, "repo_root", repoInfo.Root, "command", providerCommand)
	if err := cmd.Start(); err != nil {
		_ = database.UpdateCaptureState(ctx, captureID.Key, "error", time.Now().UTC())
		return fmt.Errorf("start provider %s: %w", provider, err)
	}

	waitErr := cmd.Wait()
	finalState := "done"
	if waitErr != nil {
		finalState = "error"
	}
	_ = database.UpdateCaptureState(context.Background(), captureID.Key, finalState, time.Now().UTC())

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), maxDuration(options.CompletionGrace, 30*time.Second))
	defer cancelDrain()
	if err := worker.ProcessPending(drainCtx); err != nil {
		r.logger.Warn("drain pending speech failed", "error", err)
	}

	return waitErr
}

func (r *Runner) PrintStatus(ctx context.Context, writer io.Writer) error {
	database, err := openStore(r.cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	return PrintStatus(ctx, writer, database)
}

func (r *Runner) PrintJobs(ctx context.Context, writer io.Writer, status string, limit int) error {
	database, err := openStore(r.cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	return PrintJobs(ctx, writer, database, status, limit)
}

func (r *Runner) Retry(ctx context.Context, jobID int64) error {
	database, err := openStore(r.cfg)
	if err != nil {
		return err
	}
	defer database.Close()
	return RetryJob(ctx, database, jobID)
}

func ParseJobID(raw string) (int64, error) {
	return parsePositiveInt64(raw)
}

func openStore(cfg config.Config) (*store.Store, error) {
	paths, err := home.Resolve(cfg.App.HomeDir)
	if err != nil {
		return nil, err
	}
	if err := paths.Ensure(); err != nil {
		return nil, err
	}
	return store.Open(paths.DB)
}

func prepareSession(ctx context.Context, cfg config.Config, logger *slog.Logger, client *bus.Client, captureID CaptureID, provider core.Provider, repoRoot string) (adapters.Session, error) {
	publisher := adapters.NewPublisher(client)
	factory, err := adapters.New(
		provider,
		adapters.Named(core.ProviderClaude, claudeadapter.NewFactory()),
		adapters.Named(core.ProviderCodex, codexadapter.NewFactory()),
		adapters.Named(core.ProviderGemini, geminiadapter.NewFactory()),
	)
	if err != nil {
		return nil, err
	}

	return factory.Prepare(ctx, adapters.PrepareParams{
		Config:    cfg,
		Logger:    logger,
		Publisher: publisher,
		Meta: adapters.SessionMeta{
			Role:      captureID.Name,
			StableID:  captureID.Key,
			Provider:  provider,
			RepoRoot:  repoRoot,
			SessionID: fmt.Sprintf("%s-%d", captureID.Key, time.Now().UTC().UnixNano()),
		},
	})
}

func resolveProviderCommand(provider core.Provider) (string, error) {
	for _, candidate := range providerCandidates(provider) {
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			if _, statErr := os.Stat(absolute); statErr == nil {
				return absolute, nil
			}
		}
	}
	return "", fmt.Errorf("could not resolve provider command for %s", provider)
}

func providerCandidates(provider core.Provider) []string {
	userHome, _ := os.UserHomeDir()
	appData := os.Getenv("AppData")

	switch provider {
	case core.ProviderClaude:
		return []string{"claude", "claude.exe", filepath.Join(userHome, ".local", "bin", "claude.exe")}
	case core.ProviderCodex:
		return []string{"codex", "codex.exe", "codex.cmd", filepath.Join(appData, "npm", "codex.cmd")}
	case core.ProviderGemini:
		return []string{"gemini", "gemini.exe", "gemini.cmd", filepath.Join(appData, "npm", "gemini.cmd")}
	default:
		return []string{string(provider)}
	}
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	merged := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]int, len(base)+len(extra))
	for _, entry := range base {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		seen[key] = len(merged)
		merged = append(merged, entry)
	}
	for key, value := range extra {
		entry := key + "=" + value
		if idx, ok := seen[key]; ok {
			merged[idx] = entry
			continue
		}
		seen[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func marshalCommand(args []string) string {
	body := "["
	for index, item := range args {
		if index > 0 {
			body += ","
		}
		body += fmt.Sprintf("%q", item)
	}
	body += "]"
	return body
}

func ensureLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func maxDuration(left time.Duration, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parsePositiveInt64(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, fmt.Errorf("value must not be empty")
	}
	var id int64
	if _, err := fmt.Sscan(value, &id); err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, fmt.Errorf("value must be positive")
	}
	return id, nil
}
