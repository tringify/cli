package appconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiffComparesMeaningNotFormatting(t *testing.T) {
	base, _ := Parse([]byte(`{"app_id":"a","store_scopes":["read_orders"],"data_field_projection":{"b":1,"a":2},"webhook_url":"https://old.example"}`))
	next, _ := Parse([]byte(`{
  "app_id": "a",
  "store_scopes": [ "read_orders" ],
  "data_field_projection": {"a": 2, "b": 1},
  "webhook_url": "https://new.example",
  "oidc_enabled": true
}`))
	got := Diff(base, next)
	want := []Change{
		{Field: "oidc_enabled", Kind: "added", To: "true"},
		{Field: "webhook_url", Kind: "changed", From: `"https://old.example"`, To: `"https://new.example"`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff = %+v", got)
	}
	if len(Diff(next, next)) != 0 {
		t.Fatal("a document differs from itself")
	}
	if got := Diff(next, base); got[0].Kind != "removed" || got[0].Field != "oidc_enabled" {
		t.Fatalf("removal = %+v", got)
	}
}

func TestParseRefusesWhatIsNotOneObject(t *testing.T) {
	for _, raw := range []string{`[]`, `null`, `{"a":1} {"b":2}`, `{`} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
}

func TestReadAndFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	formatted, err := Format([]byte(`{"schema_version":1,"app_id":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(formatted) != "{\n  \"schema_version\": 1,\n  \"app_id\": \"abc\"\n}\n" {
		t.Fatalf("format = %q", formatted)
	}
	os.WriteFile(path, formatted, 0o644)
	doc, _, err := Read(path)
	if err != nil || doc.AppID() != "abc" {
		t.Fatalf("read = %v %v", doc, err)
	}
	if long := summary([]byte(`"` + string(make([]byte, 0)) + "0123456789012345678901234567890123456789012345678901234567890123456789012345" + `"`)); len(long) != 72 {
		t.Fatalf("summary not shortened: %d", len(long))
	}
}
