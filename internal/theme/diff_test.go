package theme

import (
	"reflect"
	"testing"
)

func TestDiffComparesFileContents(t *testing.T) {
	base, err := PackageFiles(zipOf(t, map[string]string{"theme.json": "{}", "sections/hero.vasc": "a", "sections/old.vasc": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	next, err := PackageFiles(zipOf(t, map[string]string{"theme.json": "{}", "sections/hero.vasc": "b", "sections/new.vasc": "y"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{"sections/hero.vasc", "changed"}, {"sections/new.vasc", "added"}, {"sections/old.vasc", "removed"}}
	if got := Diff(base, next); !reflect.DeepEqual(got, want) {
		t.Fatalf("diff = %v", got)
	}
	if got := Diff(base, base); len(got) != 0 {
		t.Fatalf("identical packages differ: %v", got)
	}
	if _, err := PackageFiles([]byte("not a zip")); err == nil {
		t.Fatal("a non-ZIP was read as a package")
	}
}
