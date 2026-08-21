package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Favicon is a browser tab icon read from disk at startup.
//
// Read once and held in memory: it is a few kilobytes, it is requested by every
// tab, and reading it per request would put a filesystem call — and a path the
// process does not control — on a hot path for no benefit. The consequence is
// that replacing the file needs a restart, which is the same as every other
// setting in this application.
type Favicon struct {
	Body        []byte
	ContentType string

	// Path is where it is served from, base path included, with a content hash
	// in the name. The hash is what lets it be cached forever and still change
	// when the file does — an icon cached for a year is exactly the kind of
	// thing that outlives the rebrand that caused it.
	Path string
}

// contentTypes is the set of image formats a browser will actually render as a
// tab icon. Anything else is refused at startup rather than served as
// application/octet-stream, which shows as no icon at all and gives nobody a
// reason why.
var contentTypes = map[string]string{
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".ico":  "image/x-icon",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// maxFaviconBytes is a sanity bound. A tab icon is measured in kilobytes; a
// megabyte means somebody pointed this at the wrong file, and finding out at
// startup beats serving it to every visitor.
const maxFaviconBytes = 1 << 20

// LoadFavicon reads the configured icon. An empty path yields nil, which leaves
// the one the SPA ships with.
func LoadFavicon(path, base string) (*Favicon, error) {
	if path == "" {
		return nil, nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	ctype, ok := contentTypes[ext]
	if !ok {
		return nil, fmt.Errorf("ui.favicon %q: %s is not an image format browsers use for tab icons; want one of %s",
			path, orNoExtension(ext), strings.Join(extensions(), ", "))
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ui.favicon: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("ui.favicon %q is empty", path)
	}
	if len(body) > maxFaviconBytes {
		return nil, fmt.Errorf("ui.favicon %q is %d bytes; a tab icon should be a few kilobytes", path, len(body))
	}

	sum := sha256.Sum256(body)
	return &Favicon{
		Body:        body,
		ContentType: ctype,
		Path:        fmt.Sprintf("%s/favicon.%s%s", base, hex.EncodeToString(sum[:8]), ext),
	}, nil
}

func orNoExtension(ext string) string {
	if ext == "" {
		return "a file with no extension"
	}
	return ext
}

func extensions() []string {
	out := make([]string, 0, len(contentTypes))
	for ext := range contentTypes {
		out = append(out, ext)
	}
	// Sorted, so the error message reads the same twice running.
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
