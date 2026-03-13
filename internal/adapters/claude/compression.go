package claude

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

func decodeResponseBody(header http.Header, body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, nil
	}

	encodings := parseContentEncodings(header.Get("Content-Encoding"))
	if len(encodings) == 0 {
		return append([]byte(nil), body...), nil
	}

	decoded := append([]byte(nil), body...)
	var err error
	for i := len(encodings) - 1; i >= 0; i-- {
		decoded, err = decodeEncoding(encodings[i], decoded)
		if err != nil {
			return nil, fmt.Errorf("decode %s response: %w", encodings[i], err)
		}
	}

	return decoded, nil
}

func parseContentEncodings(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	encodings := make([]string, 0, len(parts))
	for _, part := range parts {
		encoding := strings.ToLower(strings.TrimSpace(part))
		if encoding == "" || encoding == "identity" {
			continue
		}
		encodings = append(encodings, encoding)
	}
	return encodings
}

func decodeEncoding(encoding string, body []byte) ([]byte, error) {
	switch encoding {
	case "gzip", "x-gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return io.ReadAll(reader)
	case "deflate":
		if decoded, err := readDeflate(body); err == nil {
			return decoded, nil
		}
		return readRawDeflate(body)
	case "br":
		return io.ReadAll(brotli.NewReader(bytes.NewReader(body)))
	default:
		return append([]byte(nil), body...), nil
	}
}

func readDeflate(body []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func readRawDeflate(body []byte) ([]byte, error) {
	reader := flate.NewReader(bytes.NewReader(body))
	defer reader.Close()
	return io.ReadAll(reader)
}
