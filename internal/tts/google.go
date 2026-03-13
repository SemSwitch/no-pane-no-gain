package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const googleEndpoint = "https://texttospeech.googleapis.com/v1/text:synthesize?key="

type GoogleBackend struct {
	APIKey   string
	Voice    string
	CacheDir string
	Client   *http.Client
}

func (b GoogleBackend) Name() string { return "google" }

func (b GoogleBackend) Speak(ctx context.Context, request Request) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("google playback backend currently expects Windows")
	}

	text := strings.TrimSpace(request.Text)
	if text == "" {
		return nil
	}

	key := strings.TrimSpace(b.APIKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("NEXIS_ECHO_GOOGLE_TTS_API_KEY"))
	}
	if key == "" {
		return fmt.Errorf("google tts api key is not configured")
	}

	voice := strings.TrimSpace(request.Voice)
	if voice == "" {
		voice = strings.TrimSpace(b.Voice)
	}
	if voice == "" {
		voice = strings.TrimSpace(os.Getenv("NEXIS_ECHO_GOOGLE_TTS_VOICE"))
	}
	if voice == "" {
		voice = "en-US-Standard-J"
	}

	payload := map[string]any{
		"input": map[string]any{
			"text": text,
		},
		"voice": map[string]any{
			"languageCode": languageCodeFromVoice(voice),
			"name":         voice,
		},
		"audioConfig": map[string]any{
			"audioEncoding": "MP3",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal google tts request: %w", err)
	}

	client := b.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleEndpoint+key, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create google tts request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request google tts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("google tts returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		AudioContent string `json:"audioContent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode google tts response: %w", err)
	}
	if decoded.AudioContent == "" {
		return fmt.Errorf("google tts returned empty audio content")
	}

	audio, err := base64.StdEncoding.DecodeString(decoded.AudioContent)
	if err != nil {
		return fmt.Errorf("decode google tts audio: %w", err)
	}

	cacheDir := strings.TrimSpace(b.CacheDir)
	if cacheDir == "" {
		cacheDir = os.TempDir()
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("create google tts cache dir: %w", err)
	}

	audioPath := filepath.Join(cacheDir, fmt.Sprintf("echo-tts-%d.mp3", time.Now().UTC().UnixNano()))
	if err := os.WriteFile(audioPath, audio, 0o644); err != nil {
		return fmt.Errorf("write google tts audio: %w", err)
	}
	defer os.Remove(audioPath)

	script := `
Add-Type -AssemblyName PresentationCore
$player = New-Object System.Windows.Media.MediaPlayer
$player.Open([System.Uri]::new($args[0]))
$player.Play()
Start-Sleep -Milliseconds 500
while (-not $player.NaturalDuration.HasTimeSpan -or $player.Position -lt $player.NaturalDuration.TimeSpan) {
  Start-Sleep -Milliseconds 100
}
$player.Close()
`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script, audioPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("play google tts audio: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func languageCodeFromVoice(voice string) string {
	parts := strings.Split(strings.TrimSpace(voice), "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return "en-US"
}
