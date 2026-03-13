package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const envPrefix = "NEXIS_ECHO_"

type Config struct {
	App        AppConfig        `json:"app"`
	NATS       NATSConfig       `json:"nats"`
	Summarizer SummarizerConfig `json:"summarizer"`
	Pipeline   PipelineConfig   `json:"pipeline"`
	TTS        TTSConfig        `json:"tts"`
	Speech     SpeechConfig     `json:"speech"`
	HTTP       HTTPConfig       `json:"http"`
	Log        LogConfig        `json:"log"`
}

type AppConfig struct {
	Name       string `json:"name"`
	HomeDir    string `json:"home_dir"`
	DataDir    string `json:"data_dir"`
	DBPath     string `json:"db_path"`
	RuntimeDir string `json:"runtime_dir"`
	LogsDir    string `json:"logs_dir"`
}

type NATSConfig struct {
	URL            string   `json:"url"`
	SubjectRoot    string   `json:"subject_root"`
	Queue          string   `json:"queue"`
	StreamName     string   `json:"stream_name"`
	StoreDir       string   `json:"store_dir"`
	ConnectTimeout Duration `json:"connect_timeout"`
	ReconnectWait  Duration `json:"reconnect_wait"`
	MaxReconnects  int      `json:"max_reconnects"`
}

type SummarizerConfig struct {
	MaxBatchSize int      `json:"max_batch_size"`
	Provider     string   `json:"provider"`
	CacheEntries int      `json:"cache_entries"`
	QueueSize    int      `json:"queue_size"`
	MinInterval  Duration `json:"min_interval"`
}

type PipelineConfig struct {
	Channels         []string `json:"channels"`
	ReconcileTimeout Duration `json:"reconcile_timeout"`
}

type TTSConfig struct {
	Voice          string   `json:"voice"`
	MaxQueued      int      `json:"max_queued"`
	DefaultBackend string   `json:"default_backend"`
	WindowsVoice   string   `json:"windows_voice"`
	GoogleAPIKey   string   `json:"google_api_key"`
	GoogleVoice    string   `json:"google_voice"`
	LeaseTTL       Duration `json:"lease_ttl"`
	PollInterval   Duration `json:"poll_interval"`
}

type SpeechConfig struct {
	DefaultMode     string `json:"default_mode"`
	ConciseMaxChars int    `json:"concise_max_chars"`
	NormalMaxChars  int    `json:"normal_max_chars"`
}

type HTTPConfig struct {
	Addr            string   `json:"addr"`
	ReadTimeout     Duration `json:"read_timeout"`
	WriteTimeout    Duration `json:"write_timeout"`
	IdleTimeout     Duration `json:"idle_timeout"`
	ShutdownTimeout Duration `json:"shutdown_timeout"`
	WebSocketPath   string   `json:"websocket_path"`
}

type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type Duration time.Duration

func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		if err := loadFile(path, &cfg); err != nil {
			return Config{}, err
		}
	}

	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}

	if err := normalize(&cfg); err != nil {
		return Config{}, err
	}

	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func Default() Config {
	return Config{
		App: AppConfig{
			Name:       "nexis-echo",
			HomeDir:    defaultHomeDir(),
			DataDir:    filepath.Join(defaultHomeDir(), "data"),
			DBPath:     filepath.Join(defaultHomeDir(), "data", "echo.db"),
			RuntimeDir: filepath.Join(defaultHomeDir(), "runtime"),
			LogsDir:    filepath.Join(defaultHomeDir(), "logs"),
		},
		NATS: NATSConfig{
			URL:            "nats://127.0.0.1:4222",
			SubjectRoot:    "nexis.echo",
			ConnectTimeout: Duration(5 * time.Second),
			ReconnectWait:  Duration(2 * time.Second),
			MaxReconnects:  -1,
			StreamName:     "ECHO",
			StoreDir:       filepath.Join(defaultHomeDir(), "runtime", "nats"),
		},
		Summarizer: SummarizerConfig{
			MaxBatchSize: 32,
			Provider:     "heuristic",
			CacheEntries: 256,
			QueueSize:    64,
			MinInterval:  Duration(100 * time.Millisecond),
		},
		Pipeline: PipelineConfig{
			Channels:         []string{"local"},
			ReconcileTimeout: Duration(2 * time.Second),
		},
		TTS: TTSConfig{
			Voice:          "default",
			MaxQueued:      32,
			DefaultBackend: "windows",
			WindowsVoice:   "",
			GoogleVoice:    "en-US-Standard-J",
			LeaseTTL:       Duration(10 * time.Minute),
			PollInterval:   Duration(750 * time.Millisecond),
		},
		Speech: SpeechConfig{
			DefaultMode:     "normal",
			ConciseMaxChars: 220,
			NormalMaxChars:  600,
		},
		HTTP: HTTPConfig{
			Addr:            "127.0.0.1:8600",
			ReadTimeout:     Duration(10 * time.Second),
			WriteTimeout:    Duration(15 * time.Second),
			IdleTimeout:     Duration(60 * time.Second),
			ShutdownTimeout: Duration(5 * time.Second),
			WebSocketPath:   "/ws",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
	}
}

func (d Duration) String() string {
	return time.Duration(d).String()
}

func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	data = bytesTrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}

	if data[0] == '"' {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}

		parsed, err := parseDuration(raw)
		if err != nil {
			return err
		}

		*d = parsed
		return nil
	}

	var seconds int64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("duration must be a string like \"5s\" or an integer number of seconds: %w", err)
	}

	*d = Duration(time.Duration(seconds) * time.Second)
	return nil
}

func loadFile(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("decode config %q: %w", path, err)
	}

	return nil
}

func applyEnv(cfg *Config) error {
	applyEnvString(envPrefix+"APP_NAME", &cfg.App.Name)
	applyEnvString(envPrefix+"HOME", &cfg.App.HomeDir)
	applyEnvString(envPrefix+"DATA_DIR", &cfg.App.DataDir)
	applyEnvString(envPrefix+"APP_DB_PATH", &cfg.App.DBPath)
	applyEnvString(envPrefix+"APP_RUNTIME_DIR", &cfg.App.RuntimeDir)
	applyEnvString(envPrefix+"APP_LOGS_DIR", &cfg.App.LogsDir)

	applyEnvString(envPrefix+"NATS_URL", &cfg.NATS.URL)
	applyEnvString(envPrefix+"NATS_SUBJECT_ROOT", &cfg.NATS.SubjectRoot)
	applyEnvString(envPrefix+"NATS_QUEUE", &cfg.NATS.Queue)
	applyEnvString(envPrefix+"NATS_STREAM_NAME", &cfg.NATS.StreamName)
	applyEnvString(envPrefix+"NATS_STORE_DIR", &cfg.NATS.StoreDir)

	if err := applyEnvDuration(envPrefix+"NATS_CONNECT_TIMEOUT", &cfg.NATS.ConnectTimeout); err != nil {
		return err
	}

	if err := applyEnvDuration(envPrefix+"NATS_RECONNECT_WAIT", &cfg.NATS.ReconnectWait); err != nil {
		return err
	}

	if err := applyEnvInt(envPrefix+"NATS_MAX_RECONNECTS", &cfg.NATS.MaxReconnects); err != nil {
		return err
	}

	if err := applyEnvInt(envPrefix+"SUMMARIZER_MAX_BATCH_SIZE", &cfg.Summarizer.MaxBatchSize); err != nil {
		return err
	}
	applyEnvString(envPrefix+"SUMMARIZER_PROVIDER", &cfg.Summarizer.Provider)
	if err := applyEnvInt(envPrefix+"SUMMARIZER_CACHE_ENTRIES", &cfg.Summarizer.CacheEntries); err != nil {
		return err
	}
	if err := applyEnvInt(envPrefix+"SUMMARIZER_QUEUE_SIZE", &cfg.Summarizer.QueueSize); err != nil {
		return err
	}
	if err := applyEnvDuration(envPrefix+"SUMMARIZER_MIN_INTERVAL", &cfg.Summarizer.MinInterval); err != nil {
		return err
	}

	if err := applyEnvCSV(envPrefix+"PIPELINE_CHANNELS", &cfg.Pipeline.Channels); err != nil {
		return err
	}
	if err := applyEnvDuration(envPrefix+"PIPELINE_RECONCILE_TIMEOUT", &cfg.Pipeline.ReconcileTimeout); err != nil {
		return err
	}

	applyEnvString(envPrefix+"TTS_VOICE", &cfg.TTS.Voice)
	applyEnvString(envPrefix+"TTS_DEFAULT_BACKEND", &cfg.TTS.DefaultBackend)
	applyEnvString(envPrefix+"TTS_WINDOWS_VOICE", &cfg.TTS.WindowsVoice)
	applyEnvString(envPrefix+"TTS_GOOGLE_API_KEY", &cfg.TTS.GoogleAPIKey)
	applyEnvString(envPrefix+"TTS_GOOGLE_VOICE", &cfg.TTS.GoogleVoice)

	if err := applyEnvInt(envPrefix+"TTS_MAX_QUEUED", &cfg.TTS.MaxQueued); err != nil {
		return err
	}
	if err := applyEnvDuration(envPrefix+"TTS_LEASE_TTL", &cfg.TTS.LeaseTTL); err != nil {
		return err
	}
	if err := applyEnvDuration(envPrefix+"TTS_POLL_INTERVAL", &cfg.TTS.PollInterval); err != nil {
		return err
	}

	applyEnvString(envPrefix+"SPEECH_DEFAULT_MODE", &cfg.Speech.DefaultMode)
	if err := applyEnvInt(envPrefix+"SPEECH_CONCISE_MAX_CHARS", &cfg.Speech.ConciseMaxChars); err != nil {
		return err
	}
	if err := applyEnvInt(envPrefix+"SPEECH_NORMAL_MAX_CHARS", &cfg.Speech.NormalMaxChars); err != nil {
		return err
	}

	applyEnvString(envPrefix+"HTTP_ADDR", &cfg.HTTP.Addr)

	if err := applyEnvDuration(envPrefix+"HTTP_READ_TIMEOUT", &cfg.HTTP.ReadTimeout); err != nil {
		return err
	}

	if err := applyEnvDuration(envPrefix+"HTTP_WRITE_TIMEOUT", &cfg.HTTP.WriteTimeout); err != nil {
		return err
	}

	if err := applyEnvDuration(envPrefix+"HTTP_IDLE_TIMEOUT", &cfg.HTTP.IdleTimeout); err != nil {
		return err
	}

	if err := applyEnvDuration(envPrefix+"HTTP_SHUTDOWN_TIMEOUT", &cfg.HTTP.ShutdownTimeout); err != nil {
		return err
	}

	applyEnvString(envPrefix+"HTTP_WEBSOCKET_PATH", &cfg.HTTP.WebSocketPath)
	applyEnvString(envPrefix+"LOG_LEVEL", &cfg.Log.Level)
	applyEnvString(envPrefix+"LOG_FORMAT", &cfg.Log.Format)

	return nil
}

func normalize(cfg *Config) error {
	cfg.App.Name = strings.TrimSpace(cfg.App.Name)
	cfg.App.HomeDir = strings.TrimSpace(cfg.App.HomeDir)
	cfg.App.DataDir = strings.TrimSpace(cfg.App.DataDir)
	cfg.App.DBPath = strings.TrimSpace(cfg.App.DBPath)
	cfg.App.RuntimeDir = strings.TrimSpace(cfg.App.RuntimeDir)
	cfg.App.LogsDir = strings.TrimSpace(cfg.App.LogsDir)
	cfg.NATS.URL = strings.TrimSpace(cfg.NATS.URL)
	cfg.NATS.SubjectRoot = strings.Trim(cfg.NATS.SubjectRoot, ". ")
	cfg.NATS.Queue = strings.TrimSpace(cfg.NATS.Queue)
	cfg.NATS.StreamName = strings.TrimSpace(cfg.NATS.StreamName)
	cfg.NATS.StoreDir = strings.TrimSpace(cfg.NATS.StoreDir)
	cfg.Summarizer.Provider = strings.ToLower(strings.TrimSpace(cfg.Summarizer.Provider))
	cfg.TTS.Voice = strings.TrimSpace(cfg.TTS.Voice)
	cfg.TTS.DefaultBackend = strings.ToLower(strings.TrimSpace(cfg.TTS.DefaultBackend))
	cfg.TTS.WindowsVoice = strings.TrimSpace(cfg.TTS.WindowsVoice)
	cfg.TTS.GoogleAPIKey = strings.TrimSpace(cfg.TTS.GoogleAPIKey)
	cfg.TTS.GoogleVoice = strings.TrimSpace(cfg.TTS.GoogleVoice)
	cfg.Speech.DefaultMode = strings.ToLower(strings.TrimSpace(cfg.Speech.DefaultMode))
	cfg.HTTP.Addr = strings.TrimSpace(cfg.HTTP.Addr)
	cfg.HTTP.WebSocketPath = strings.TrimSpace(cfg.HTTP.WebSocketPath)
	cfg.Log.Level = strings.ToLower(strings.TrimSpace(cfg.Log.Level))
	cfg.Log.Format = strings.ToLower(strings.TrimSpace(cfg.Log.Format))
	cfg.Pipeline.Channels = trimSlice(cfg.Pipeline.Channels)

	if cfg.App.HomeDir == "" {
		cfg.App.HomeDir = defaultHomeDir()
	}
	if !filepath.IsAbs(cfg.App.HomeDir) {
		absolutePath, err := filepath.Abs(cfg.App.HomeDir)
		if err != nil {
			return fmt.Errorf("resolve home dir %q: %w", cfg.App.HomeDir, err)
		}
		cfg.App.HomeDir = absolutePath
	}
	if cfg.App.DataDir == "" {
		cfg.App.DataDir = filepath.Join(cfg.App.HomeDir, "data")
	}
	if cfg.App.DBPath == "" {
		cfg.App.DBPath = filepath.Join(cfg.App.DataDir, "echo.db")
	}
	if cfg.App.RuntimeDir == "" {
		cfg.App.RuntimeDir = filepath.Join(cfg.App.HomeDir, "runtime")
	}
	if cfg.App.LogsDir == "" {
		cfg.App.LogsDir = filepath.Join(cfg.App.HomeDir, "logs")
	}
	if cfg.NATS.StoreDir == "" {
		cfg.NATS.StoreDir = filepath.Join(cfg.App.RuntimeDir, "nats")
	}

	paths := []*string{
		&cfg.App.DataDir,
		&cfg.App.DBPath,
		&cfg.App.RuntimeDir,
		&cfg.App.LogsDir,
		&cfg.NATS.StoreDir,
	}
	for _, path := range paths {
		if *path == "" || filepath.IsAbs(*path) {
			continue
		}
		absolutePath, err := filepath.Abs(*path)
		if err != nil {
			return fmt.Errorf("resolve path %q: %w", *path, err)
		}
		*path = absolutePath
	}

	return nil
}

func validate(cfg Config) error {
	var errs []error

	if cfg.App.Name == "" {
		errs = append(errs, errors.New("app.name must not be empty"))
	}

	if cfg.App.HomeDir == "" {
		errs = append(errs, errors.New("app.home_dir must not be empty"))
	}

	if cfg.App.DataDir == "" {
		errs = append(errs, errors.New("app.data_dir must not be empty"))
	}

	absPaths := map[string]string{
		"app.home_dir":    cfg.App.HomeDir,
		"app.data_dir":    cfg.App.DataDir,
		"app.db_path":     cfg.App.DBPath,
		"app.runtime_dir": cfg.App.RuntimeDir,
		"app.logs_dir":    cfg.App.LogsDir,
		"nats.store_dir":  cfg.NATS.StoreDir,
	}
	for label, value := range absPaths {
		if value != "" && !filepath.IsAbs(value) {
			errs = append(errs, fmt.Errorf("%s must be absolute: %q", label, value))
		}
	}

	if cfg.NATS.URL == "" {
		errs = append(errs, errors.New("nats.url must not be empty"))
	} else if parsed, err := url.Parse(cfg.NATS.URL); err != nil {
		errs = append(errs, fmt.Errorf("nats.url is invalid: %w", err))
	} else if parsed.Scheme == "" || parsed.Host == "" {
		errs = append(errs, fmt.Errorf("nats.url must include scheme and host: %q", cfg.NATS.URL))
	}

	if cfg.NATS.SubjectRoot == "" {
		errs = append(errs, errors.New("nats.subject_root must not be empty"))
	}
	if cfg.NATS.StreamName == "" {
		errs = append(errs, errors.New("nats.stream_name must not be empty"))
	}
	if cfg.NATS.StoreDir == "" {
		errs = append(errs, errors.New("nats.store_dir must not be empty"))
	}

	if time.Duration(cfg.NATS.ConnectTimeout) <= 0 {
		errs = append(errs, errors.New("nats.connect_timeout must be greater than zero"))
	}

	if time.Duration(cfg.NATS.ReconnectWait) <= 0 {
		errs = append(errs, errors.New("nats.reconnect_wait must be greater than zero"))
	}

	if cfg.NATS.MaxReconnects < -1 {
		errs = append(errs, errors.New("nats.max_reconnects must be -1 or greater"))
	}

	if cfg.Summarizer.MaxBatchSize <= 0 {
		errs = append(errs, errors.New("summarizer.max_batch_size must be greater than zero"))
	}
	if cfg.Summarizer.Provider == "" {
		errs = append(errs, errors.New("summarizer.provider must not be empty"))
	}
	if cfg.Summarizer.CacheEntries <= 0 {
		errs = append(errs, errors.New("summarizer.cache_entries must be greater than zero"))
	}
	if cfg.Summarizer.QueueSize <= 0 {
		errs = append(errs, errors.New("summarizer.queue_size must be greater than zero"))
	}
	if time.Duration(cfg.Summarizer.MinInterval) < 0 {
		errs = append(errs, errors.New("summarizer.min_interval must be zero or greater"))
	}

	if len(cfg.Pipeline.Channels) == 0 {
		errs = append(errs, errors.New("pipeline.channels must include at least one channel"))
	}
	if time.Duration(cfg.Pipeline.ReconcileTimeout) <= 0 {
		errs = append(errs, errors.New("pipeline.reconcile_timeout must be greater than zero"))
	}

	if cfg.TTS.Voice == "" {
		errs = append(errs, errors.New("tts.voice must not be empty"))
	}

	if cfg.TTS.MaxQueued <= 0 {
		errs = append(errs, errors.New("tts.max_queued must be greater than zero"))
	}
	switch cfg.TTS.DefaultBackend {
	case "windows", "google":
	default:
		errs = append(errs, fmt.Errorf("tts.default_backend must be windows or google: %q", cfg.TTS.DefaultBackend))
	}
	if time.Duration(cfg.TTS.LeaseTTL) <= 0 {
		errs = append(errs, errors.New("tts.lease_ttl must be greater than zero"))
	}
	if time.Duration(cfg.TTS.PollInterval) <= 0 {
		errs = append(errs, errors.New("tts.poll_interval must be greater than zero"))
	}
	switch cfg.Speech.DefaultMode {
	case "concise", "normal", "heavy":
	default:
		errs = append(errs, fmt.Errorf("speech.default_mode must be concise, normal, or heavy: %q", cfg.Speech.DefaultMode))
	}
	if cfg.Speech.ConciseMaxChars <= 0 {
		errs = append(errs, errors.New("speech.concise_max_chars must be greater than zero"))
	}
	if cfg.Speech.NormalMaxChars <= 0 {
		errs = append(errs, errors.New("speech.normal_max_chars must be greater than zero"))
	}

	if cfg.HTTP.Addr == "" {
		errs = append(errs, errors.New("http.addr must not be empty"))
	} else if _, _, err := net.SplitHostPort(cfg.HTTP.Addr); err != nil {
		errs = append(errs, fmt.Errorf("http.addr is invalid: %w", err))
	}

	if time.Duration(cfg.HTTP.ReadTimeout) <= 0 {
		errs = append(errs, errors.New("http.read_timeout must be greater than zero"))
	}

	if time.Duration(cfg.HTTP.WriteTimeout) <= 0 {
		errs = append(errs, errors.New("http.write_timeout must be greater than zero"))
	}

	if time.Duration(cfg.HTTP.IdleTimeout) <= 0 {
		errs = append(errs, errors.New("http.idle_timeout must be greater than zero"))
	}

	if time.Duration(cfg.HTTP.ShutdownTimeout) <= 0 {
		errs = append(errs, errors.New("http.shutdown_timeout must be greater than zero"))
	}

	if cfg.HTTP.WebSocketPath == "" {
		errs = append(errs, errors.New("http.websocket_path must not be empty"))
	} else if !strings.HasPrefix(cfg.HTTP.WebSocketPath, "/") {
		errs = append(errs, fmt.Errorf("http.websocket_path must start with '/': %q", cfg.HTTP.WebSocketPath))
	}

	switch cfg.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log.level must be debug, info, warn, or error: %q", cfg.Log.Level))
	}

	switch cfg.Log.Format {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("log.format must be text or json: %q", cfg.Log.Format))
	}

	return errors.Join(errs...)
}

func applyEnvString(key string, target *string) {
	if value, ok := os.LookupEnv(key); ok {
		*target = value
	}
}

func applyEnvDuration(key string, target *Duration) error {
	value, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}

	parsed, err := parseDuration(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	*target = parsed
	return nil
}

func applyEnvInt(key string, target *int) error {
	value, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}

	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("%s: parse int: %w", key, err)
	}

	*target = parsed
	return nil
}

func applyEnvCSV(key string, target *[]string) error {
	value, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}

	*target = strings.Split(value, ",")
	return nil
}

func parseDuration(value string) (Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, errors.New("duration must not be empty")
	}

	if seconds, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return Duration(time.Duration(seconds) * time.Second), nil
	}

	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", value, err)
	}

	return Duration(parsed), nil
}

func defaultDataDir() string {
	return filepath.Join(defaultHomeDir(), "data")
}

func defaultHomeDir() string {
	if executablePath, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(executablePath), "nexis-echo-home")
	}

	if localData, err := os.UserCacheDir(); err == nil {
		return filepath.Join(localData, "NexisEcho")
	}

	if workingDir, err := os.Getwd(); err == nil {
		return filepath.Join(workingDir, "nexis-echo-home")
	}

	return filepath.Join(".", "nexis-echo-home")
}

func bytesTrimSpace(data []byte) []byte {
	start := 0
	for start < len(data) && isSpace(data[start]) {
		start++
	}

	end := len(data)
	for end > start && isSpace(data[end-1]) {
		end--
	}

	return data[start:end]
}

func isSpace(value byte) bool {
	switch value {
	case ' ', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}

func trimSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}

	return out
}
