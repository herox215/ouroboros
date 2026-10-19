package claudecode

import (
	"encoding/json"
	"regexp"
	"strings"
)

// The API caches a conversation from its beginning up to the first token
// that differs from a request before, and the CLI does not build two
// requests of one conversation alike: it adds context of its own to the
// newest message of the user (the e-mail address of the account, as a
// reminder of the system), and it tells the date at the end of the message
// that describes where it runs in the first request, and in a message of
// its own at the end in every later one. Both change what the request
// begins with from call to call, and nothing of the conversation would be
// read from the cache. stabilize puts the requests in one form: the messages
// of the user as ouro sent them, and the date at the end.
//
// What does not have the form it expects is left as the CLI built it: a CLI
// that builds its requests otherwise costs the cache, not the answer.

// dateLine is the line the CLI tells the date in.
var dateLine = regexp.MustCompile(`\s*Today's date is \d{4}-\d{2}-\d{2}\.\s*$`)

// environment starts the message that tells where the CLI runs.
const environment = "# Environment"

// message is a message of a request, with all its fields.
type message map[string]json.RawMessage

func (m message) role() string {
	var role string
	json.Unmarshal(m["role"], &role) // a message without one has none
	return role
}

// texts are the texts of the content of m, as it is a text or blocks of
// text; false if it has another block.
func (m message) texts() (string, json.RawMessage, bool) {
	var s string
	if json.Unmarshal(m["content"], &s) == nil {
		return s, nil, true
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(m["content"], &blocks) != nil {
		return "", nil, false
	}
	var b strings.Builder
	var mark json.RawMessage
	for _, block := range blocks {
		var t struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		json.Unmarshal(raw(block), &t) // read once already
		if t.Type != "text" {
			return "", nil, false
		}
		b.WriteString(t.Text)
		if block["cache_control"] != nil {
			mark = block["cache_control"]
		}
	}
	return b.String(), mark, true
}

// stabilize puts the messages of a request in the form described above,
// with the frames ouro sent.
func stabilize(fields map[string]json.RawMessage, frames []frame) {
	var msgs []message
	if json.Unmarshal(fields["messages"], &msgs) != nil {
		return
	}
	restore(msgs, frames)
	msgs = dateAtEnd(msgs)
	fields["messages"] = raw(msgs)
}

// restore makes the messages of the user in msgs those of frames, as ouro
// sent them: one for one, as long as the messages besides those of the
// system are as many as the frames and of the same roles. A mark of the
// cache the CLI put on a message moves to the last block of what replaces
// it.
func restore(msgs []message, frames []frame) {
	var talk []message
	for _, m := range msgs {
		if m.role() != "system" {
			talk = append(talk, m)
		}
	}
	if len(talk) != len(frames) {
		return
	}
	for i, m := range talk {
		if m.role() != frames[i].Type {
			return
		}
	}
	for i, m := range talk {
		if frames[i].Type != "user" {
			continue
		}
		content := frames[i].Message.Content
		if mark := lastMark(m["content"]); mark != nil && len(content) > 0 {
			content = append(content[:len(content)-1:len(content)-1], marked(content[len(content)-1], mark))
		}
		m["content"] = raw(content)
	}
}

// lastMark is the last mark of the cache on a block of content, nil for
// none.
func lastMark(content json.RawMessage) json.RawMessage {
	var blocks []map[string]json.RawMessage
	json.Unmarshal(content, &blocks) // a text has no mark
	var mark json.RawMessage
	for _, b := range blocks {
		if b["cache_control"] != nil {
			mark = b["cache_control"]
		}
	}
	return mark
}

// marked is block with the mark of the cache mark.
func marked(block, mark json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(block, &fields) != nil {
		return block
	}
	fields["cache_control"] = mark
	return raw(fields)
}

// dateAtEnd takes the date out of the message that tells where the CLI runs,
// and tells it in a message of the system at the end, unless one there tells
// it already. That message keeps a mark of the cache, on a block of its text
// as in the first request; in a later one it was a text, which is the same to
// the model and to the cache.
func dateAtEnd(msgs []message) []message {
	for _, m := range msgs {
		if m.role() != "system" {
			continue
		}
		text, mark, ok := m.texts()
		if !ok || !strings.HasPrefix(text, environment) {
			continue
		}
		date := dateLine.FindString(text)
		block := map[string]any{"type": "text", "text": strings.TrimSuffix(text, date)}
		if mark != nil {
			block["cache_control"] = mark
		}
		m["content"] = raw([]any{block})
		if date == "" {
			return msgs
		}
		if last, _, _ := msgs[len(msgs)-1].texts(); msgs[len(msgs)-1].role() == "system" && dateLine.MatchString(last) {
			return msgs
		}
		return append(msgs, message{"role": raw("system"), "content": raw(strings.TrimSpace(date))})
	}
	return msgs
}
