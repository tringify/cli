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
		case canonical(from) != canonical(to):
			changes = append(changes, Change{Field: name, Kind: "changed", From: summary(from), To: summary(to)})
		}
	}
	return changes
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
