package tts

type GoogleConfig struct {
	APIKey     string
	Voice      string
	RuntimeDir string
}

func NewWindows(_ string) Backend {
	return WindowsBackend{}
}

func NewGoogle(cfg GoogleConfig) Backend {
	return GoogleBackend{
		APIKey:   cfg.APIKey,
		Voice:    cfg.Voice,
		CacheDir: cfg.RuntimeDir,
	}
}
