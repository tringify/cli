package theme

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Change is one file that differs between two theme packages.
type Change struct {
	Path string
	Kind string // "added", "changed" or "removed"
}

// PackageFiles returns the SHA-256 of every file in a theme package (ZIP),
// keyed by its path.
func PackageFiles(data []byte) (map[string]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("the theme package is not a ZIP: %w", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") || !f.Mode().IsRegular() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		sum := sha256.New()
		_, err = io.Copy(sum, io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		files[f.Name] = hex.EncodeToString(sum.Sum(nil))
	}
	return files, nil
}

// Diff lists the files that are added, changed or removed in next compared
// with base, sorted by path.
func Diff(base, next map[string]string) []Change {
	var out []Change
	for path, sum := range next {
		if old, ok := base[path]; !ok {
			out = append(out, Change{Path: path, Kind: "added"})
		} else if old != sum {
			out = append(out, Change{Path: path, Kind: "changed"})
		}
	}
	for path := range base {
		if _, ok := next[path]; !ok {
			out = append(out, Change{Path: path, Kind: "removed"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
