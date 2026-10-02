package themekit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tringify/cli/internal/pyjson"
)

// SourceExt is the extension of compiled Vascula sections.
const SourceExt = ".vasc"

// readText is Path.read_text(encoding="utf-8"): strict UTF-8 with
// universal newlines.
func readText(root, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", osError(root, err)
	}
	if !utf8.Valid(data) {
		return "", utf8Error(data)
	}
	return pyjson.NormalizeNewlines(string(data)), nil
}

func utf8Error(data []byte) error {
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size <= 1 {
			reason := "invalid start byte"
			if data[i] >= 0xc2 && data[i] <= 0xf4 {
				if i+1 >= len(data) {
					reason = "unexpected end of data"
				} else {
					reason = "invalid continuation byte"
				}
			}
			return fmt.Errorf("'utf-8' codec can't decode byte 0x%02x in position %d: %s", data[i], i, reason)
		}
		i += size
	}
	return errors.New("'utf-8' codec can't decode the file")
}

// readJSON reads a JSON file, naming the file, line and column on error.
func readJSON(path, root string) (pyjson.Value, error) {
	text, err := readText(root, path)
	if err != nil {
		return nil, err
	}
	v, err := pyjson.Loads(text)
	var de *pyjson.DecodeError
	if errors.As(err, &de) {
		return nil, fmt.Errorf("%s:%d:%d: %s", rel(root, path), de.Line, de.Column, de.Msg)
	}
	return v, err
}

// pyStrip is Python's str.strip(): Unicode whitespace, including the
// information separators U+001C–U+001F that Go does not treat as space.
func pyStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sortNames orders directory entries the way Python sorts Path objects:
// by code point, or case-insensitively on Windows.
func sortNames(names []string) {
	if runtime.GOOS == "windows" {
		sort.SliceStable(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
		return
	}
	sort.Strings(names)
}

// Build compiles src/sections/<name>/{body.html,style.css,schema.json} into
// sections/<name>.vasc, refreshes the section index in theme.json and
// records the generated files in src/.generated-sections.json. It returns
// the built section names.
func Build(root string) ([]string, error) {
	root, err := resolvePath(root)
	if err != nil {
		return nil, err
	}
	sourceRoot := filepath.Join(root, "src", "sections")
	outputRoot := filepath.Join(root, "sections")
	for _, name := range []string{"src", "src/sections", "src/_shared.css", "src/.generated-sections.json", "sections", "theme.json"} {
		if isSymlink(filepath.Join(root, filepath.FromSlash(name))) {
			return nil, fmt.Errorf("theme source symlinks are not supported: %s", name)
		}
	}
	sharedCSS, err := readText(root, filepath.Join(root, "src", "_shared.css"))
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(root, "theme.json")
	manifestValue, err := readJSON(manifestPath, root)
	if err != nil {
		return nil, err
	}
	manifest, ok := manifestValue.(*pyjson.Object)
	var entries []pyjson.Value
	if ok {
		sections, present := manifest.Get("sections")
		if !present {
			entries = []pyjson.Value{}
		} else if entries, ok = sections.([]pyjson.Value); !ok {
			ok = false
		}
	}
	if !ok {
		return nil, errors.New("theme.json must contain a sections list")
	}
	index := map[string]*pyjson.Object{}
	for _, e := range entries {
		obj, isObj := e.(*pyjson.Object)
		if !isObj {
			return nil, errors.New("theme.json sections must be named objects")
		}
		name, isStr := obj.Get("name")
		if _, s := name.(pyjson.String); !isStr || !s {
			return nil, errors.New("theme.json sections must be named objects")
		}
	}
	for _, e := range entries {
		obj := e.(*pyjson.Object)
		name, _ := obj.Get("name")
		index[string(name.(pyjson.String))] = obj
	}
	if len(index) != len(entries) {
		return nil, errors.New("theme.json contains duplicate section names")
	}

	ownershipPath := filepath.Join(root, "src", ".generated-sections.json")
	previous := map[string]string{}
	if _, err := os.Stat(ownershipPath); err == nil {
		stateValue, err := readJSON(ownershipPath, root)
		if err != nil {
			return nil, err
		}
		state, ok := stateValue.(*pyjson.Object)
		var sections *pyjson.Object
		if ok {
			version, _ := state.Get("version")
			s, _ := state.Get("sections")
			sections, ok = s.(*pyjson.Object)
			ok = ok && pyjson.IsInt(version, 1)
		}
		if !ok {
			return nil, errors.New("invalid generated-section ownership file")
		}
		for _, name := range sections.Keys() {
			digest, _ := sections.Get(name)
			d, isStr := digest.(pyjson.String)
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || !isStr {
				return nil, errors.New("invalid generated-section ownership entry")
			}
			previous[name] = string(d)
		}
	}

	dirEntries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return nil, osError(root, err)
	}
	var names []string
	for _, entry := range dirEntries {
		if isDir(filepath.Join(sourceRoot, entry.Name())) {
			names = append(names, entry.Name())
		}
	}
	sortNames(names)

	var built []string
	outputs := map[string]string{}
	var generated []pyjson.Value
	for _, name := range names {
		sourceDir := filepath.Join(sourceRoot, name)
		if isSymlink(sourceDir) {
			return nil, fmt.Errorf("theme source symlinks are not supported: %s", name)
		}
		for _, part := range []string{"body.html", "style.css", "schema.json"} {
			if isSymlink(filepath.Join(sourceDir, part)) {
				return nil, fmt.Errorf("theme source symlinks are not supported: %s", name)
			}
		}
		body, err := readText(root, filepath.Join(sourceDir, "body.html"))
		if err != nil {
			return nil, err
		}
		style, err := readText(root, filepath.Join(sourceDir, "style.css"))
		if err != nil {
			return nil, err
		}
		schemaValue, err := readJSON(filepath.Join(sourceDir, "schema.json"), root)
		if err != nil {
			return nil, err
		}
		schema, ok := schemaValue.(*pyjson.Object)
		if !ok {
			return nil, fmt.Errorf("%s: schema must be an object", name)
		}
		if schemaName, _ := schema.Get("name"); !pyjson.Equal(schemaName, pyjson.String(name)) {
			return nil, fmt.Errorf("%s: schema name must match the section directory", name)
		}
		outputs[name] = "<style>\n" + sharedCSS + "\n" + pyStrip(style) + "\n</style>\n" + pyStrip(body) + "\n" +
			"{% schema %}\n" + pyjson.Dumps(schema) + "\n{% endschema %}\n"
		entry := pyjson.NewObject()
		if existing, ok := index[name]; ok {
			entry = existing.Copy()
		}
		kind, ok := schema.Get("kind")
		if !ok {
			kind = pyjson.String("section")
		}
		entry.Set("name", pyjson.String(name))
		entry.Set("kind", kind)
		entry.Set("file", pyjson.String("sections/"+name+SourceExt))
		entry.Set("origin", pyjson.String("override"))
		if target, _ := schema.Get("target"); pyjson.Truthy(target) {
			entry.Set("target", target)
		} else {
			entry.Delete("target")
		}
		generated = append(generated, entry)
		built = append(built, name)
	}

	var stale []string
	for name := range previous {
		if _, ok := outputs[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range append(append([]string{}, built...), stale...) {
		if isSymlink(filepath.Join(outputRoot, name+SourceExt)) {
			return nil, fmt.Errorf("theme output symlinks are not supported: %s", name)
		}
	}
	for _, name := range stale {
		path := filepath.Join(outputRoot, name+SourceExt)
		if exists(path) {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, osError(root, err)
			}
			if sha256Hex(data) != previous[name] {
				return nil, fmt.Errorf("%s: previously generated section was edited; reconcile it before removing its source", name)
			}
		}
	}
	if err := os.MkdirAll(outputRoot, 0o777); err != nil {
		return nil, osError(root, err)
	}
	for _, name := range built {
		if err := os.WriteFile(filepath.Join(outputRoot, name+SourceExt), []byte(outputs[name]), 0o666); err != nil {
			return nil, osError(root, err)
		}
	}
	for _, name := range stale {
		if err := os.Remove(filepath.Join(outputRoot, name+SourceExt)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, osError(root, err)
		}
	}
	isStale := map[string]bool{}
	for _, name := range stale {
		isStale[name] = true
	}
	sections := []pyjson.Value{}
	for _, e := range entries {
		name, _ := e.(*pyjson.Object).Get("name")
		n := string(name.(pyjson.String))
		if _, built := outputs[n]; !built && !isStale[n] {
			sections = append(sections, e)
		}
	}
	sections = append(sections, generated...)
	manifest.Set("sections", sections)
	if err := os.WriteFile(manifestPath, []byte(pyjson.Dumps(manifest)+"\n"), 0o666); err != nil {
		return nil, osError(root, err)
	}
	owned := pyjson.NewObject()
	for _, name := range built {
		owned.Set(name, pyjson.String(sha256Hex([]byte(outputs[name]))))
	}
	state := pyjson.NewObject()
	state.Set("version", pyjson.Int("1"))
	state.Set("sections", owned)
	if err := os.WriteFile(ownershipPath, []byte(pyjson.Dumps(state)+"\n"), 0o666); err != nil {
		return nil, osError(root, err)
	}
	if built == nil {
		built = []string{}
	}
	return built, nil
}
