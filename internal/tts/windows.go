package tts

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type WindowsBackend struct{}

func (WindowsBackend) Name() string { return "windows" }

func (WindowsBackend) Speak(ctx context.Context, request Request) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("windows tts is only available on Windows")
	}

	text := strings.TrimSpace(request.Text)
	if text == "" {
		return nil
	}

	script := `$text = [Console]::In.ReadToEnd()
Add-Type -AssemblyName System.Speech
$synth = New-Object System.Speech.Synthesis.SpeechSynthesizer
if ($env:NEXIS_ECHO_TTS_VOICE) {
  try { $synth.SelectVoice($env:NEXIS_ECHO_TTS_VOICE) } catch {}
}
$synth.Speak($text)
$synth.Dispose()
`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if voice := strings.TrimSpace(request.Voice); voice != "" {
		cmd.Env = append(cmd.Environ(), "NEXIS_ECHO_TTS_VOICE="+voice)
	}
	cmd.Stdin = strings.NewReader(text)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("windows tts: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
