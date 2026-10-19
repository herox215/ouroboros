package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary is also the CLI the tests run: with fakeEnv set it acts as
// Claude Code, as far as ouro uses it. What it does is told by these
// variables of its environment:
//
//	FAKE_LOG      a directory it writes its arguments, environment and stdin to
//	FAKE_STATUS   what "auth status" says, or a file it reads that from
//	FAKE_CONTROL  the response to a control request, its request_id filled in
//	FAKE_MODE     what a call does once its stdin is closed, see fakeCall
const fakeEnv = "FAKE_CLAUDE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) != "" {
		os.Exit(fakeClaude(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeClaude is the fake CLI, run with args.
func fakeClaude(args []string) int {
	log := os.Getenv("FAKE_LOG")
	if log != "" {
		n := 1
		for ; ; n++ {
			if _, err := os.Stat(filepath.Join(log, fmt.Sprintf("args-%d.json", n))); err != nil {
				break
			}
		}
		b, _ := json.Marshal(args)
		os.WriteFile(filepath.Join(log, fmt.Sprintf("args-%d.json", n)), b, 0o600)
		os.WriteFile(filepath.Join(log, fmt.Sprintf("env-%d.txt", n)), []byte(strings.Join(os.Environ(), "\n")), 0o600)
		wd, _ := os.Getwd()
		os.WriteFile(filepath.Join(log, fmt.Sprintf("wd-%d.txt", n)), []byte(wd), 0o600)
	}
	if len(args) == 2 && args[0] == "auth" && args[1] == "status" {
		st := os.Getenv("FAKE_STATUS")
		if b, err := os.ReadFile(st); err == nil {
			st = string(b)
		}
		fmt.Println(st)
		if !strings.Contains(st, `"loggedIn":true`) {
			return 1
		}
		return 0
	}
	mode := os.Getenv("FAKE_MODE")
	switch mode {
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "stderr":
		for i := range 25 {
			fmt.Fprintf(os.Stderr, "line %d %s\n\n", i, strings.Repeat("x", 30))
		}
		return 1
	}
	fmt.Println("a banner that is no JSON")
	fmt.Println(`{"type":"system","subtype":"init"}`)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(nil, 64<<20)
	var frames []frame
	for in.Scan() {
		line := in.Text()
		if log != "" {
			f, _ := os.OpenFile(filepath.Join(log, "stdin.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			fmt.Fprintln(f, line)
			f.Close()
		}
		var head struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		json.Unmarshal([]byte(line), &head)
		if head.Type == "control_request" {
			fmt.Println(`{"type":"control_response","response":{"subtype":"success","request_id":"another"}}`)
			fmt.Printf(`{"type":"control_response","response":%s}`+"\n", strings.ReplaceAll(os.Getenv("FAKE_CONTROL"), "ID", head.RequestID))
			return 0
		}
		var f frame
		json.Unmarshal([]byte(line), &f)
		if mode == "noack" {
			return 1
		}
		frames = append(frames, f)
		if f.ShouldQuery != nil {
			fmt.Println(`{"type":"system","subtype":"status"}`)
			if mode == "badack" {
				fmt.Println(`{"type":"result","subtype":"success","num_turns":1}`)
				continue
			}
			fmt.Println(`{"type":"result","subtype":"success","num_turns":0}`)
		}
	}
	return fakeCall(mode, args, frames)
}

// fakeCall does what the CLI does with the conversation frames once its
// stdin is closed, as mode says:
//
//	"" or "answer"  sends one request to the API and tells the result
//	"twice"         sends a second one after it, which is refused
//	"hello"         asks for /api/hello first, as native does
//	"nojson"        sends a request that is no JSON
//	"short"         sends a request that breaks off
//	"auth", "limit", "other"  answers an error itself, without a request
//	"fail", "failerrs", "failnone"  ends with a result that failed
//	"silent"        ends without a word
func fakeCall(mode string, args []string, frames []frame) int {
	base := os.Getenv("ANTHROPIC_BASE_URL")
	switch mode {
	case "silent":
		return 0
	case "auth":
		fmt.Println(`{"type":"assistant","error":"authentication_failed","message":{"content":[{"type":"text","text":"Not logged in"}]}}`)
		fmt.Println(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in"}`)
		return 1
	case "limit":
		fmt.Println(`{"type":"assistant","error":"rate_limit","message":{"content":[{"type":"text","text":"You've hit your limit"}]}}`)
		return 1
	case "other":
		fmt.Println(`{"type":"assistant","error":"billing_error","message":{"content":[]}}`)
		return 1
	case "fail":
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`)
		return 1
	case "failerrs":
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["one","two"]}`)
		return 1
	case "failnone":
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true}`)
		return 1
	case "hello":
		resp, err := http.Get(base + "/api/hello")
		if err == nil {
			resp.Body.Close()
		}
	case "short":
		u, _ := url.Parse(base)
		conn, err := net.Dial("tcp", u.Host)
		if err == nil {
			fmt.Fprintf(conn, "POST %s/v1/messages HTTP/1.1\r\nHost: %s\r\nContent-Length: 100\r\n\r\n{\"model\"", u.Path, u.Host)
			time.Sleep(100 * time.Millisecond)
			conn.Close()
		}
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"broke"}`)
		return 1
	}
	model := args[slices.Index(args, "--model")+1]
	body := nativeBody(model, frames)
	if mode == "nojson" {
		body = []byte("not json")
	}
	post := func() int {
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages?beta=true", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer the-login-of-the-cli")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	status := post()
	if mode == "twice" {
		post()
	}
	fmt.Println(`{"type":"assistant","message":{"content":[{"type":"text","text":"as the CLI saw it"}]}}`)
	fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"result":"status %s"}`+"\n", strconv.Itoa(status))
	return 0
}

// nativeBody is a request as native builds it of frames: on the first
// request of a conversation the reminder of the account is the first block
// of the message of the user, and the date ends the message about where it
// runs; on a later one the reminder ends the newest message of the user and
// the date is a message of its own at the end.
func nativeBody(model string, frames []frame) []byte {
	reminder := "<system-reminder>\nthe e-mail of the account\n</system-reminder>\n"
	first := len(frames) == 1
	var msgs []any
	for i, f := range frames {
		content := slices.Clone(f.Message.Content)
		if f.Type == "user" && i == len(frames)-1 {
			if first {
				content = append([]json.RawMessage{text(reminder)}, content...)
			} else {
				content = append(content, text(reminder))
			}
		}
		msgs = append(msgs, map[string]any{"role": f.Type, "content": content})
		if i == 0 {
			if first {
				msgs = append(msgs, map[string]any{"role": "system", "content": []any{map[string]any{"type": "text",
					"text": "# Environment\nwhere it runs\n\nToday's date is 2026-10-19.", "cache_control": map[string]string{"type": "ephemeral"}}}})
			} else {
				msgs = append(msgs, map[string]any{"role": "system", "content": "# Environment\nwhere it runs"})
			}
		}
	}
	if !first {
		msgs = append(msgs, map[string]any{"role": "system", "content": []any{map[string]any{"type": "text",
			"text": "Today's date is 2026-10-19.", "cache_control": map[string]string{"type": "ephemeral"}}}})
	}
	return raw(map[string]any{"model": model, "system": []any{map[string]string{"type": "text", "text": "native"}},
		"messages": msgs, "tools": []any{}, "stream": true})
}
