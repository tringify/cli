package appconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// App blocks keep their Vascula source in files beside the configuration:
// each entry of storefront_blocks names its file in source_file
// ("blocks/<key>.vasc") where the Developer API has the source inline. Load
// reads the files in; Split writes them out.

// BlocksField is the configuration field that lists the app's blocks.
const BlocksField = "storefront_blocks"

// BlocksDir is where pull writes block sources, next to the configuration.
const BlocksDir = "blocks"

// maxBlockFile bounds one block source file; the platform's own limit is
// lower and is checked by `tringify app block check`.
const maxBlockFile = 1 << 20

var blockKey = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

// field is one member of a JSON object, in document order.
type field struct {
	name  string
	value json.RawMessage
}

// fields reads a JSON object's members in order.
func fields(raw []byte) ([]field, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := decoder.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []field
	for decoder.More() {
		tok, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, _ := tok.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, field{name, value})
	}
	return out, nil
}

func object(members []field) []byte {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		name, _ := json.Marshal(m.name)
		out.Write(name)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')
	return out.Bytes()
}

// mapBlocks rewrites each block entry of a configuration with change,
// keeping every other field and the field order as they are.
func mapBlocks(raw []byte, change func(i int, block []field) ([]field, error)) ([]byte, error) {
	top, err := fields(raw)
	if err != nil {
		return nil, err
	}
	for t := range top {
		if top[t].name != BlocksField || string(top[t].value) == "null" {
			continue
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(top[t].value, &entries); err != nil {
			return nil, fmt.Errorf("%s must be a list", BlocksField)
		}
		list := make([][]byte, len(entries))
		for i, entry := range entries {
			block, err := fields(entry)
			if err != nil {
				return nil, fmt.Errorf("%s entry %d is not an object", BlocksField, i+1)
			}
			if block, err = change(i, block); err != nil {
				return nil, err
			}
			list[i] = object(block)
		}
		top[t].value = append(append([]byte{'['}, bytes.Join(list, []byte{','})...), ']')
	}
	return Format(object(top))
}

func stringField(block []field, name string) (string, bool) {
	for _, f := range block {
		if f.name == name {
			var s string
			if json.Unmarshal(f.value, &s) != nil {
				return "", false
			}
			return s, true
		}
	}
	return "", false
}

// BlockFile is a block source file, relative to the configuration's
// directory with forward slashes.
type BlockFile struct {
	Key, Path string
	Source    []byte
}

// Split turns a configuration as the API serves it into the file Load
// reads: each block's source moves to blocks/<key>.vasc, named by
// source_file in place of source.
func Split(raw []byte) ([]byte, []BlockFile, error) {
	var files []BlockFile
	out, err := mapBlocks(raw, func(i int, block []field) ([]field, error) {
		key, _ := stringField(block, "key")
		if !blockKey.MatchString(key) || len(key) > 40 {
			return nil, fmt.Errorf("block %d has key %q, which cannot name a file", i+1, key)
		}
		source, ok := stringField(block, "source")
		if !ok {
			return nil, fmt.Errorf("block %q has no source", key)
		}
		name := BlocksDir + "/" + key + ".vasc"
		files = append(files, BlockFile{Key: key, Path: name, Source: []byte(source)})
		for j := range block {
			if block[j].name == "source" {
				block[j] = field{"source_file", mustJSON(name)}
			}
		}
		return block, nil
	})
	return out, files, err
}

func mustJSON(v any) json.RawMessage {
	out, _ := json.Marshal(v)
	return out
}

// Load reads the configuration at file with each block's source read from
// its source_file. It returns the document and its JSON as the API takes
// it, and each block's file.
func Load(file string) (Document, []byte, []BlockFile, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, err := Parse(raw); err != nil {
		return nil, nil, nil, fmt.Errorf("%s is %w", file, err)
	}
	dir := filepath.Dir(file)
	var files []BlockFile
	inline, err := mapBlocks(raw, func(i int, block []field) ([]field, error) {
		key, _ := stringField(block, "key")
		label := fmt.Sprintf("block %q", key)
		if key == "" {
			label = fmt.Sprintf("block %d", i+1)
		}
		if _, ok := stringField(block, "source"); ok {
			return nil, fmt.Errorf("%s: %s keeps its source in a file; replace \"source\" with \"source_file\": \"%s/%s.vasc\"", file, label, BlocksDir, key)
		}
		name, ok := stringField(block, "source_file")
		if !ok || name == "" {
			return nil, fmt.Errorf("%s: %s needs \"source_file\", the path of its .vasc file, such as \"%s/%s.vasc\"", file, label, BlocksDir, key)
		}
		clean := path.Clean(name)
		if path.IsAbs(name) || strings.Contains(name, `\`) || clean == ".." || strings.HasPrefix(clean, "../") || clean != name {
			return nil, fmt.Errorf("%s: %s source_file %q must be a relative path inside the project, such as \"%s/%s.vasc\"", file, label, name, BlocksDir, key)
		}
		local := filepath.Join(dir, filepath.FromSlash(name))
		info, err := os.Stat(local)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", file, label, err)
		}
		if !info.Mode().IsRegular() || info.Size() > maxBlockFile {
			return nil, fmt.Errorf("%s: %s source_file %s must be a file of at most 1 MiB", file, label, name)
		}
		source, err := os.ReadFile(local)
		if err != nil {
			return nil, err
		}
		files = append(files, BlockFile{Key: key, Path: name, Source: source})
		for j := range block {
			if block[j].name == "source_file" {
				block[j] = field{"source", mustJSON(string(source))}
			}
		}
		return block, nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	doc, err := Parse(inline)
	if err != nil {
		return nil, nil, nil, err
	}
	return doc, inline, files, nil
}

// Write saves a configuration as the API serves it: the file with
// source_file references, and each block's source in its file. It returns
// the block files that were written and the .vasc files in blocks/ that no
// block names any more, which it leaves in place.
func Write(file string, raw []byte) (written []string, unused []string, err error) {
	doc, files, err := Split(raw)
	if err != nil {
		return nil, nil, err
	}
	dir := filepath.Dir(file)
	named := map[string]bool{}
	for _, f := range files {
		named[f.Path] = true
		local := filepath.Join(dir, filepath.FromSlash(f.Path))
		if current, err := os.ReadFile(local); err == nil && bytes.Equal(current, f.Source) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(local, f.Source, 0o644); err != nil {
			return nil, nil, err
		}
		written = append(written, f.Path)
	}
	if err := os.WriteFile(file, doc, 0o644); err != nil {
		return nil, nil, err
	}
	entries, _ := os.ReadDir(filepath.Join(dir, BlocksDir))
	for _, e := range entries {
		name := BlocksDir + "/" + e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".vasc") && !named[name] {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	return written, unused, nil
}

// SourceFiles maps each block key in a configuration file to its
// source_file, for pointing at the file a problem is in. Blocks without
// one are left out.
func SourceFiles(file string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(file)
	if err != nil {
		return out
	}
	_, _ = mapBlocks(raw, func(_ int, block []field) ([]field, error) {
		key, _ := stringField(block, "key")
		if name, ok := stringField(block, "source_file"); ok {
			out[key] = name
		}
		return block, nil
	})
	return out
}

// AppendBlock adds block, a declaration with its source inline, to the end
// of a configuration's storefront_blocks.
func AppendBlock(raw []byte, block any) ([]byte, error) {
	top, err := fields(raw)
	if err != nil {
		return nil, err
	}
	entry, err := json.Marshal(block)
	if err != nil {
		return nil, err
	}
	for t := range top {
		if top[t].name != BlocksField {
			continue
		}
		var entries []json.RawMessage
		if string(top[t].value) != "null" {
			if err := json.Unmarshal(top[t].value, &entries); err != nil {
				return nil, fmt.Errorf("%s must be a list", BlocksField)
			}
		}
		top[t].value = mustJSON(append(entries, entry))
		return Format(object(top))
	}
	top = append(top, field{BlocksField, mustJSON([]json.RawMessage{entry})})
	return Format(object(top))
}
