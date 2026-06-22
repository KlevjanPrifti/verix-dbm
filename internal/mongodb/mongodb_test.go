package mongodb

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestCommandName(t *testing.T) {
	cases := map[string]string{
		`{"find":"users","limit":5}`:           "find",
		`{ "aggregate": "t", "pipeline": [] }`: "aggregate",
		`{"dropDatabase":1}`:                   "dropdatabase",
		`{"Drop":"users"}`:                     "drop", // case-insensitive
	}
	for in, want := range cases {
		got, err := CommandName(in)
		if err != nil {
			t.Errorf("CommandName(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("CommandName(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := CommandName(`{}`); err == nil {
		t.Error("empty command document should error")
	}
	if _, err := CommandName(`not json`); err == nil {
		t.Error("invalid JSON should error")
	}
}

func TestReadAllowedAndConfirm(t *testing.T) {
	for _, name := range []string{"find", "aggregate", "count", "listcollections", "ping"} {
		if !ReadAllowed(name) {
			t.Errorf("%q should be read-allowed", name)
		}
	}
	for _, name := range []string{"insert", "update", "delete", "drop", "dropdatabase"} {
		if ReadAllowed(name) {
			t.Errorf("%q must NOT be read-allowed", name)
		}
	}
	for _, name := range []string{"drop", "dropdatabase", "dropindexes", "shutdown", "killop"} {
		if !NeedsConfirm(name) {
			t.Errorf("%q should require confirmation", name)
		}
	}
	for _, name := range []string{"find", "insert", "update"} {
		if NeedsConfirm(name) {
			t.Errorf("%q should not require confirmation", name)
		}
	}
}

func TestPrettyJSON(t *testing.T) {
	doc, err := bson.Marshal(bson.D{{Key: "a", Value: 1}, {Key: "b", Value: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	out := prettyJSON(bson.Raw(doc))
	if !strings.Contains(out, `"a"`) || !strings.Contains(out, "\n") {
		t.Errorf("expected indented JSON, got %q", out)
	}
}

func TestParseDocEmpty(t *testing.T) {
	m, err := parseDoc("")
	if err != nil || len(m) != 0 {
		t.Errorf("empty filter should parse to empty doc, got %v, %v", m, err)
	}
	if _, err := parseDoc("{bad"); err == nil {
		t.Error("malformed filter should error")
	}
}
