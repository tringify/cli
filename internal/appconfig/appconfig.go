// Package appconfig reads, writes and compares tringify.app.json, the app
// configuration document the Developer API serves.
package appconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// FileName is the configuration file's default name.
const FileName = "tringify.app.json"

// Document is a configuration document as raw top-level fields.
type Document map[string]json.RawMessage

// Parse reads a document and checks it is a JSON object.
func Parse(raw []byte) (Document, error) {
	var doc Document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if doc == nil {
		return nil, errors.New("not a JSON object")
	}
	if decoder.More() {
		return nil, errors.New("more than one JSON value")
	}
	return doc, nil
}

// Read loads the document at path.
func Read(path string) (Document, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	doc, err := Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is %w", path, err)
	}
	return doc, raw, nil
}

// AppID is the document's app_id, or "" when it has none.
func (d Document) AppID() string {
	var id string
	_ = json.Unmarshal(d["app_id"], &id)
	return id
}

// Format renders a document the way the CLI writes it: two-space indent,
// fields in the order the API returns them, a trailing newline.
func Format(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	if err := json.Indent(&out, bytes.TrimSpace(raw), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// Change is one top-level field that differs.
type Change struct {
	Field string
	Kind  string // added, changed or removed
	From  string
	To    string
}

// Diff lists the fields that differ from base to next, sorted by field.
// Values are compared by meaning, not formatting.
func Diff(base, next Document) []Change {
	fields := map[string]bool{}
	for k := range base {
		fields[k] = true
	}
	for k := range next {
		fields[k] = true
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	var changes []Change
	for _, name := range names {
		from, inBase := base[name]
		to, inNext := next[name]
		switch {
		case !inBase:
			changes = append(changes, Change{Field: name, Kind: "added", To: summary(to)})
		case !inNext:
			changes = append(changes, Change{Field: name, Kind: "removed", From: summary(from)})
		case canonical(from) == canonical(to):
		case name == BlocksField:
			changes = append(changes, diffBlocks(from, to)...)
		default:
			changes = append(changes, Change{Field: name, Kind: "changed", From: summary(from), To: summary(to)})
		}
	}
	return changes
}

// diffBlocks lists block changes one block field at a time, sources by
// their length.
func diffBlocks(from, to json.RawMessage) []Change {
	parse := func(raw json.RawMessage) ([]string, map[string]map[string]json.RawMessage) {
		var list []map[string]json.RawMessage
		_ = json.Unmarshal(raw, &list)
		var keys []string
		byKey := map[string]map[string]json.RawMessage{}
		for i, block := range list {
			var key string
			if json.Unmarshal(block["key"], &key) != nil || key == "" {
				key = fmt.Sprintf("#%d", i+1)
			}
			if _, seen := byKey[key]; !seen {
				keys = append(keys, key)
			}
			byKey[key] = block
		}
		return keys, byKey
	}
	fromKeys, before := parse(from)
	toKeys, after := parse(to)
	var changes []Change
	for _, key := range fromKeys {
		if _, ok := after[key]; !ok {
			changes = append(changes, Change{Field: BlocksField + " " + key, Kind: "removed", From: "block"})
		}
	}
	for _, key := range toKeys {
		old, ok := before[key]
		if !ok {
			changes = append(changes, Change{Field: BlocksField + " " + key, Kind: "added", To: "block"})
			continue
		}
		var names []string
		for name := range old {
			names = append(names, name)
		}
		for name := range after[key] {
			if _, ok := old[name]; !ok {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			a, b := old[name], after[key][name]
			if canonical(a) == canonical(b) {
				continue
			}
			describe := summary
			if name == "source" {
				describe = lines
			}
			change := Change{Field: BlocksField + " " + key + "." + name, Kind: "changed", From: describe(a), To: describe(b)}
			switch {
			case a == nil:
				change.Kind = "added"
			case b == nil:
				change.Kind = "removed"
			}
			changes = append(changes, change)
		}
	}
	return changes
}

// lines describes a block source by its length.
func lines(raw json.RawMessage) string {
	var source string
	_ = json.Unmarshal(raw, &source)
	n := strings.Count(source, "\n")
	if !strings.HasSuffix(source, "\n") && source != "" {
		n++
	}
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func canonical(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, _ := json.Marshal(v) // map keys come out sorted
	return string(out)
}

// summary is a one-line rendering of a value, shortened when long.
func summary(raw json.RawMessage) string {
	s := canonical(raw)
	if len(s) > 72 {
		s = s[:69] + "..."
	}
	return strings.TrimSpace(s)
}
