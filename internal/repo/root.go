package repo

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Info struct {
	Root string
	Hash string
}

func Detect(start string) (Info, error) {
	if strings.TrimSpace(start) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Info{}, fmt.Errorf("get working directory: %w", err)
		}
		start = wd
	}

	absolute, err := filepath.Abs(start)
	if err != nil {
		return Info{}, fmt.Errorf("resolve start path %q: %w", start, err)
	}

	root := absolute
	for {
		if exists(filepath.Join(root, ".git")) {
			return newInfo(root), nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return newInfo(absolute), nil
		}
		root = parent
	}
}

func newInfo(root string) Info {
	normalized := normalize(root)
	sum := sha1.Sum([]byte(normalized))
	return Info{
		Root: normalized,
		Hash: hex.EncodeToString(sum[:8]),
	}
}

func normalize(value string) string {
	clean := filepath.Clean(strings.TrimSpace(value))
	if runtime.GOOS == "windows" {
		clean = strings.ToLower(clean)
	}
	return clean
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

