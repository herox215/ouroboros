package history

import (
	"strings"
	"testing"

	"ouroboros/internal/openai"
)

// Match returns the conversations whose records hold what is looked for,
// ignoring case, as List names them; a query that goes on from the one
// before is looked for only in what matched then.
func TestMatch(t *testing.T) {
	a := Open(t.TempDir())
	say := func(session, text string) {
		if err := a.Append(Who{Session: session}, []openai.Message{{Role: "user", Content: text}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	say("s1", "how the Cache works")
	say("s2", "something else entirely")
	got := func(query, want string) {
		t.Helper()
		hits, err := a.Match(query)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(hits, ",") != want {
			t.Fatalf("%q: %q, want %q", query, hits, want)
		}
	}
	got("cache", "owner/s1")
	got("cached", "") // of "cache": only what matched then is read again
	got("entirely", "owner/s2")
	say("s3", "the mesh and the cache")
	got("mesh cache", "owner/s3") // every word, wherever it is
	got("mesh cache", "owner/s3") // and again, from what matched the first word
	if hits, err := a.Match("  "); hits != nil || err != nil {
		t.Fatalf("nothing looked for: %q, %v", hits, err)
	}
}
