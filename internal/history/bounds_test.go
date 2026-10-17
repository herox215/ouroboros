package history

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/openai"
)

// The size of a conversation is that of its records as lines of JSON, as its
// legacy file had it.
func TestSize(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, answer("read my notes", "the notes", "Done."), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, r := range all(t, a, owner) {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		want += int64(len(b) + 1)
	}
	if entries, err := a.List(); err != nil || len(entries) != 1 || entries[0].Size != want {
		t.Fatalf("entries %+v, %v; want a size of %d", entries, err, want)
	}
}

// An offset is within the first record: its last byte is, the byte after it
// is not.
func TestReadOffsetAtTheEnd(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	first := show(all(t, a, owner)[0])
	if out, err := a.Read(owner, "", 1, 1, len(first)); err == nil {
		t.Errorf("offset after the record: %q", out)
	}
	if out, err := a.Read(owner, "", 1, 1, len(first)-1); err != nil || out != "\n[records 1 to 1 of 2: continue with from 2]" {
		t.Errorf("offset of the last byte: %q, %v", out, err)
	}
}

// A read takes whole records as long as they fit into MaxReadBytes with the
// note, and not one more.
func TestReadFillsThePage(t *testing.T) {
	a := at(t)
	const size = 400 // of each record as Read shows it
	var msgs []openai.Message
	for seq := 1; seq <= 50; seq++ {
		head := len(show(Record{Seq: seq, Answer: 1, Role: "user", Time: time.Now()}))
		msgs = append(msgs, openai.Message{Role: "user", Content: strings.Repeat("x", size-head-1)})
	}
	if err := a.Append(owner, msgs, nil); err != nil {
		t.Fatal(err)
	}
	out, err := a.Read(owner, "", 1, MaxRecords, 0)
	fits := (MaxReadBytes - readNote) / size
	note := fmt.Sprintf("[records 1 to %d of 51: continue with from %d]", fits, fits+1)
	if err != nil || len(out) > MaxReadBytes || !strings.HasSuffix(out, note) || len(out) != fits*size+len(note) {
		t.Fatalf("read of %d bytes ending %q, %v; want %q", len(out), out[max(0, len(out)-80):], err, note)
	}
}

// A page holds records up to exactly 128 KiB of JSON lines, and not a byte
// more.
func TestPageExactBound(t *testing.T) {
	one, err := json.Marshal(Record{Seq: 100, Role: "user", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ line, want int }{{64 << 10, 2}, {64<<10 + 1, 1}} {
		a := pageArchive(t, 2, c.line-len(one)) // the line has its break, the record "x"
		p, err := a.Page("owner/page", 0)
		if err != nil || len(p.Records) != c.want || p.More != (c.want == 1) {
			t.Fatalf("lines of %d bytes: %d records, more %v, %v", c.line, len(p.Records), p.More, err)
		}
		a.Close()
	}
}

// What is searched for with three ASCII characters or more is looked up in
// the index of the search, which fails once it is gone; anything else is
// looked for record by record.
func TestSearchUsesTheIndex(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, a, "DROP TABLE history_fts")
	for _, query := range []string{"roo", "e r", "~ro", "Roof?"} {
		if _, _, err := a.Search(owner, query, false, 0); err == nil {
			t.Errorf("%q was not looked up in the index", query)
		}
	}
	for _, query := range []string{"ro", "ré", "\x1froo", "\x7froo", "röof"} {
		if _, _, err := a.Search(owner, query, false, 0); err != nil {
			t.Errorf("%q: %v", query, err)
		}
	}
}

// A call is the first one of its ID, and its result the first one after it:
// an ID can come again, as some providers count them anew in every answer,
// and a result can come without its call, as of an answer that broke off.
func TestToolResultOfAnIDThatComesAgain(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	call := func(arguments string) openai.ToolCall {
		return openai.ToolCall{ID: "1", Function: openai.FunctionCall{Name: "read", Arguments: arguments}}
	}
	orphan := []openai.Message{{Role: "tool", ToolCallID: "1", Content: "of no call"}}
	stopped := []openai.Message{{Role: "assistant", ToolCalls: []openai.ToolCall{call("first"), call("same record")}}}
	again := []openai.Message{{Role: "assistant", ToolCalls: []openai.ToolCall{call("next answer")}}, {Role: "tool", ToolCallID: "1", Content: "result"}}
	for _, msgs := range [][]openai.Message{orphan, stopped, again} {
		if err := a.Append(Who{Session: "s"}, msgs, nil); err != nil {
			t.Fatal(err)
		}
	}
	c, content, found, err := a.ToolResult("owner/s", "1")
	if err != nil || c.Arguments != "first" || content != "result" || !found {
		t.Fatalf("result: %+v %q %v %v", c, content, found, err)
	}
}
