package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/SemSwitch/Nexis-Echo/internal/config"
	"github.com/SemSwitch/Nexis-Echo/internal/core"
	"github.com/SemSwitch/Nexis-Echo/internal/echo"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	command := "run"
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "status", "jobs", "retry":
			command = args[0]
			args = args[1:]
		}
	}

	var (
		configPath string
		home       string
		showVer    bool
	)

	fs := flag.NewFlagSet("nexis-echo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&configPath, "config", "", "optional path to a JSON config file")
	fs.StringVar(&home, "home", "", "optional runtime home directory")
	fs.BoolVar(&showVer, "version", false, "print version and exit")

	switch command {
	case "status":
		if err := fs.Parse(args); err != nil {
			return 2
		}
	case "jobs":
		var statusFilter string
		var limit int
		fs.StringVar(&statusFilter, "status", "", "optional job status filter")
		fs.IntVar(&limit, "limit", 100, "maximum jobs to print")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if showVer {
			fmt.Println(version)
			return 0
		}
		cfg, logger, ctx, stop, err := loadRuntime(configPath, home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nexis-echo: %v\n", err)
			return 1
		}
		defer stop()
		return runJobs(ctx, cfg, logger, statusFilter, limit)
	case "retry":
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if showVer {
			fmt.Println(version)
			return 0
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "nexis-echo retry requires a job id")
			return 2
		}
		cfg, logger, ctx, stop, err := loadRuntime(configPath, home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nexis-echo: %v\n", err)
			return 1
		}
		defer stop()
		return runRetry(ctx, cfg, logger, fs.Arg(0))
	default:
		var captureName string
		var llm string
		var mode string
		var repoRoot string
		var backend string
		fs.StringVar(&captureName, "capture", "", "name for this capture session")
		fs.StringVar(&llm, "llm", "", "provider to capture (codex, claude, gemini)")
		fs.StringVar(&mode, "mode", "", "speech mode (concise, normal, heavy)")
		fs.StringVar(&repoRoot, "repo", "", "optional repo root override")
		fs.StringVar(&backend, "backend", "", "tts backend override (windows or google)")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if showVer {
			fmt.Println(version)
			return 0
		}
		if captureName == "" || llm == "" {
			fmt.Fprintln(os.Stderr, "nexis-echo requires --capture and --llm")
			return 2
		}
		provider, err := core.ParseProvider(llm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nexis-echo: %v\n", err)
			return 2
		}
		cfg, logger, ctx, stop, err := loadRuntime(configPath, home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nexis-echo: %v\n", err)
			return 1
		}
		defer stop()
		return runCapture(ctx, cfg, logger, echo.RunOptions{
			CaptureName: captureName,
			Provider:    provider,
			Mode:        mode,
			RepoRoot:    repoRoot,
			Backend:     backend,
			Args:        fs.Args(),
		})
	}

	if showVer {
		fmt.Println(version)
		return 0
	}

	cfg, logger, ctx, stop, err := loadRuntime(configPath, home)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexis-echo: %v\n", err)
		return 1
	}
	defer stop()
	return runStatus(ctx, cfg, logger)
}

func loadRuntime(configPath string, home string) (config.Config, *slog.Logger, context.Context, context.CancelFunc, error) {
	if home != "" {
		if err := os.Setenv("NEXIS_ECHO_HOME", home); err != nil {
			return config.Config{}, nil, nil, nil, fmt.Errorf("set NEXIS_ECHO_HOME: %w", err)
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, nil, nil, nil, fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg.Log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return cfg, logger, ctx, stop, nil
}

func runCapture(ctx context.Context, cfg config.Config, logger *slog.Logger, options echo.RunOptions) int {
	runner := echo.NewRunner(cfg, logger)
	if err := runner.RunCapture(ctx, options); err != nil {
		logger.Error("capture run failed", "error", err)
		return 1
	}
	return 0
}

func runStatus(ctx context.Context, cfg config.Config, logger *slog.Logger) int {
	runner := echo.NewRunner(cfg, logger)
	if err := runner.PrintStatus(ctx, os.Stdout); err != nil {
		logger.Error("status command failed", "error", err)
		return 1
	}
	return 0
}

func runJobs(ctx context.Context, cfg config.Config, logger *slog.Logger, status string, limit int) int {
	runner := echo.NewRunner(cfg, logger)
	if err := runner.PrintJobs(ctx, os.Stdout, status, limit); err != nil {
		logger.Error("jobs command failed", "error", err)
		return 1
	}
	return 0
}

func runRetry(ctx context.Context, cfg config.Config, logger *slog.Logger, rawJobID string) int {
	jobID, err := echo.ParseJobID(rawJobID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexis-echo: invalid job id %q: %v\n", rawJobID, err)
		return 2
	}

	runner := echo.NewRunner(cfg, logger)
	if err := runner.Retry(ctx, jobID); err != nil {
		logger.Error("retry command failed", "job_id", jobID, "error", err)
		return 1
	}
	return 0
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, opts)
	default:
		handler = slog.NewTextHandler(os.Stderr, opts)
	}

	return slog.New(handler)
}
