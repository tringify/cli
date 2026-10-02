package pyjson

import (
	"errors"
	"math"
	"testing"
)

func TestRoundTripMatchesPythonDumps(t *testing.T) {
	cases := map[string]string{
		`{"b": 1, "a": [1.0, 2.5e-7, 1e16, 123456789012345678901234567890, -0, -0.0, true, null], "é": "😀\u2028\u007f"}`: "{\n  \"b\": 1,\n  \"a\": [\n    1.0,\n    2.5e-07,\n    1e+16,\n    123456789012345678901234567890,\n    0,\n    -0.0,\n    true,\n    null\n  ],\n  \"\\u00e9\": \"\\ud83d\\ude00\\u2028\\u007f\"\n}",
		`{"a": 1, "a": 2, "b": {}}`:         "{\n  \"a\": 2,\n  \"b\": {}\n}",
		`[NaN, Infinity, -Infinity, 1E400]`: "[\n  NaN,\n  Infinity,\n  -Infinity,\n  Infinity\n]",
		`"\ud800"`:                          `"\ud800"`,
		`[]`:                                `[]`,
		`1e-5`:                              `1e-05`,
		`100000000000000000000.0`:           `1e+20`,
		`0.0001`:                            `0.0001`,
		`1234567890123456.0`:                `1234567890123456.0`,
	}
	for in, want := range cases {
		v, err := Loads(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := Dumps(v); got != want {
			t.Errorf("Dumps(%s)\n got %s\nwant %s", in, got, want)
		}
	}
}

func TestUnicodeAndCompactForms(t *testing.T) {
	v, _ := Loads(`{"k": ["é", "\u0000\t", "\u007f"]}`)
	if got := DumpsUnicode(v); got != "{\n  \"k\": [\n    \"é\",\n    \"\\u0000\\t\",\n    \"\u007f\"\n  ]\n}" {
		t.Errorf("DumpsUnicode = %q", got)
	}
	if got := Compact(v); got != `{"k": ["\u00e9", "\u0000\t", "\u007f"]}` {
		t.Errorf("Compact = %q", got)
	}
	if got := Repr(v); got != `{'k': ['é', '\x00\t', '\x7f']}` {
		t.Errorf("Repr = %q", got)
	}
}

func TestErrorsMatchPython(t *testing.T) {
	cases := map[string]string{
		"":              "Expecting value: line 1 column 1 (char 0)",
		"{invalid":      "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)",
		"{\n  \"a\" 1}": "Expecting ':' delimiter: line 2 column 7 (char 8)",
		"[1 2]":         "Expecting ',' delimiter: line 1 column 4 (char 3)",
		"[1,]":          "Expecting value: line 1 column 4 (char 3)",
		"{\"a\":1,}":    "Expecting property name enclosed in double quotes: line 1 column 8 (char 7)",
		"\"abc":         "Unterminated string starting at: line 1 column 1 (char 0)",
		"\"a\tb\"":      "Invalid control character at: line 1 column 3 (char 2)",
		"\"\\q\"":       "Invalid \\escape: line 1 column 2 (char 1)",
		"\"\\u12\"":     "Invalid \\uXXXX escape: line 1 column 3 (char 2)",
		"true false":    "Extra data: line 1 column 6 (char 5)",
		"\ufeff{}":      "Unexpected UTF-8 BOM (decode using utf-8-sig): line 1 column 1 (char 0)",
		"01":            "Extra data: line 1 column 2 (char 1)",
		"[\"é\", x]":    "Expecting value: line 1 column 7 (char 6)",
	}
	for in, want := range cases {
		_, err := Loads(in)
		var de *DecodeError
		if !errors.As(err, &de) || err.Error() != want {
			t.Errorf("Loads(%q) = %v, want %s", in, err, want)
		}
	}
}

func TestPythonEquality(t *testing.T) {
	if !Equal(Int("3"), Float(3)) || !Equal(true, Int("1")) || Equal(String("1"), Int("1")) || Equal(Float(math.NaN()), Float(math.NaN())) {
		t.Fatal("numeric equality differs from Python")
	}
	if !Truthy(Float(0.5)) || Truthy(Int("0")) || Truthy(String("")) || Truthy([]Value{}) || Truthy(NewObject()) {
		t.Fatal("truthiness differs from Python")
	}
}
