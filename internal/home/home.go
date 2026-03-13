package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const EnvHome = "NEXIS_ECHO_HOME"

type Paths struct {
	Root    string
	Config  string
	Data    string
	Runtime string
	Logs    string
	Cache   string
	DB      string
}

func Resolve(override string) (Paths, error) {
	root := strings.TrimSpace(override)
	if root == "" {
		root = strings.TrimSpace(os.Getenv(EnvHome))
	}
	if root == "" {
		executable, err := os.Executable()
		if err != nil {
			workingDir, wdErr := os.Getwd()
			if wdErr != nil {
				return Paths{}, fmt.Errorf("resolve default home: executable: %w; working directory: %v", err, wdErr)
			}
			root = filepath.Join(workingDir, "data")
		} else {
			root = filepath.Join(filepath.Dir(executable), "data")
		}
	}

	absolute, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home %q: %w", root, err)
	}

	return Paths{
		Root:    absolute,
		Config:  filepath.Join(absolute, "config"),
		Data:    filepath.Join(absolute, "data"),
		Runtime: filepath.Join(absolute, "runtime"),
		Logs:    filepath.Join(absolute, "logs"),
		Cache:   filepath.Join(absolute, "cache"),
		DB:      filepath.Join(absolute, "echo.db"),
	}, nil
}

func (p Paths) Ensure() error {
	dirs := []string{
		p.Root,
		p.Config,
		p.Data,
		p.Runtime,
		p.Logs,
		p.Cache,
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	return nil
}

