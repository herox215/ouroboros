package browser

// Native API contract: github.com/jo-inc/camofox-browser, server.js and
// AGENTS.md (inspected 2026-10-15). This is REST, not Firefox CDP. We use
// userId as the isolation boundary, not sessionKey (only a tab grouping).
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"ouroboros/plugin"
)

type camoBackend struct {
	settings  settings
	client    *http.Client
	namespace string
	mu        sync.Mutex
	convs     map[string]*camoConversation
}

type camoConversation struct {
	mu      sync.Mutex
	user    string
	tabs    []*camoTab
	current int
}

type camoTab struct {
	id, url  string
	snapshot string // converted refs, cached for byte-offset continuation
	refs     map[int]string
	selects  map[int]bool // native combobox/listbox refs require /select, not fill
}

func newCamo(s settings) *camoBackend {
	var nonce [16]byte
	// crypto/rand.Read cannot fail on supported Go platforms.
	rand.Read(nonce[:])
	return &camoBackend{settings: s, client: remoteHTTP(), namespace: hex.EncodeToString(nonce[:]), convs: map[string]*camoConversation{}}
}

func (b *camoBackend) conversation(key string) *camoConversation {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.convs[key]
	if c == nil {
		hash := sha256.Sum256([]byte(key))
		c = &camoConversation{user: "ouro-" + b.namespace + "-" + hex.EncodeToString(hash[:16])}
		b.convs[key] = c
	}
	return c
}

// request always supplies userId in both query and JSON: current native
// routes differ in which they inspect (DELETE historically used query).
func (b *camoBackend) request(ctx context.Context, method, path, user string, params map[string]any, out any) ([]byte, error) {
	endpoint, _ := url.Parse(b.settings.Endpoint)
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	q := endpoint.Query()
	q.Set("userId", user)
	endpoint.RawQuery = q.Encode()
	if method == http.MethodGet {
		for name, value := range params {
			q.Set(name, fmt.Sprint(value))
		}
		endpoint.RawQuery = q.Encode()
	}
	var body io.Reader
	if method != http.MethodGet {
		if params == nil {
			params = map[string]any{}
		}
		params["userId"] = user
		data, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.settings.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.settings.Token)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Camofox request failed (check endpoint and connectivity)")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponse {
		return nil, errors.New("Camofox response exceeds 16 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		json.Unmarshal(data, &failure)
		message := failure.Error
		if len(message) > 1000 {
			message = message[:1000]
		}
		if b.settings.Token != "" {
			message = strings.ReplaceAll(message, b.settings.Token, "[redacted]")
		}
		return nil, fmt.Errorf("Camofox %s returned HTTP %d: %s", method, resp.StatusCode, message)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("invalid Camofox JSON response: %w", err)
		}
		var failed struct {
			Error string `json:"error"`
			OK    *bool  `json:"ok"`
		}
		json.Unmarshal(data, &failed)
		if failed.Error != "" || (failed.OK != nil && !*failed.OK) {
			return nil, errors.New("Camofox reported an unsuccessful operation")
		}
	}
	return data, nil
}

func camoPath(t *camoTab, action string) string {
	p := "/tabs/" + url.PathEscape(t.id)
	if action != "" {
		p += "/" + action
	}
	return p
}

func (b *camoBackend) run(ctx context.Context, key, target string, a args) (string, error) {
	switch {
	case a.Action == "dialog", a.Action == "console":
		return "", fmt.Errorf("Camofox native REST does not support browser action %q", a.Action)
	case a.Action == "open" && a.Element != 0:
		return "", errors.New("Camofox native REST cannot resolve a snapshot ref to a link/image URL for open; use click, or open with an explicit URL")
	}
	c := b.conversation(key)
	if err := waitLock(ctx, c.mu.TryLock); err != nil {
		return "", err
	}
	defer c.mu.Unlock()
	if a.Action == "tabs" {
		if err := b.reconcileTabs(ctx, c); err != nil {
			return "", err
		}
		if len(c.tabs) == 0 {
			return "the conversation has no tab", nil
		}
		lines := make([]string, len(c.tabs))
		for i, t := range c.tabs {
			lines[i] = fmt.Sprintf("[%d] %s", i+1, t.url)
			if i == c.current {
				lines[i] += " (current)"
			}
		}
		return strings.Join(lines, "\n"), nil
	}
	if a.Action == "close" {
		if len(c.tabs) == 0 {
			return "there is no tab to close", nil
		}
		t := c.tabs[c.current]
		var result map[string]any
		if _, err := b.request(ctx, http.MethodDelete, camoPath(t, ""), c.user, nil, &result); err != nil {
			return "", err
		}
		c.tabs = slices.Delete(c.tabs, c.current, c.current+1)
		if len(c.tabs) == 0 {
			c.current = 0
			return "closed the tab", nil
		}
		c.current = len(c.tabs) - 1
		return fmt.Sprintf("closed the tab; tab %d of %d is the current one, at %s", c.current+1, len(c.tabs), c.tabs[c.current].url), nil
	}
	if a.Action == "switch" {
		if a.Tab < 1 || a.Tab > len(c.tabs) {
			return "", fmt.Errorf("there is no tab %d: the conversation has %d", a.Tab, len(c.tabs))
		}
		c.current = a.Tab - 1
		return fmt.Sprintf("switched to tab %d; now at %s", a.Tab, c.tabs[c.current].url), nil
	}
	if a.Action == "open" && (len(c.tabs) == 0 || a.NewTab) {
		var created struct {
			TabID string `json:"tabId"`
		}
		_, err := b.request(ctx, http.MethodPost, "/tabs", c.user, map[string]any{"sessionKey": "ouro", "url": target}, &created)
		if err != nil {
			return "", err
		}
		if !camoID.MatchString(created.TabID) {
			return "", errors.New("Camofox create response lacks a safe tabId")
		}
		c.tabs = append(c.tabs, &camoTab{id: created.TabID, url: target})
		c.current = len(c.tabs) - 1
		return b.read(ctx, c, c.tabs[c.current], 0)
	}
	if len(c.tabs) == 0 {
		return "", errors.New("Camofox has no current tab: open a URL first")
	}
	t := c.tabs[c.current]
	if a.Action == "read" {
		return b.read(ctx, c, t, a.Offset)
	}
	if a.Action == "screenshot" {
		data, err := b.request(ctx, http.MethodGet, camoPath(t, "screenshot"), c.user, nil, nil)
		if err != nil {
			return "", err
		}
		dimensions, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "", errors.New("Camofox screenshot was not a valid PNG")
		}
		if !plugin.ShowImage(ctx, plugin.Image{Type: "image/png", Data: data}) {
			return "", errors.New("a screenshot is shown only to an agent that answers")
		}
		return fmt.Sprintf("a screenshot of %s, %d×%d pixels", t.url, dimensions.Width, dimensions.Height), nil
	}
	params := map[string]any{}
	action := a.Action
	switch a.Action {
	case "open":
		action = "navigate"
		params["url"] = target
	case "click", "type":
		ref, ok := t.refs[a.Element]
		if !ok {
			return "", fmt.Errorf("there is no element [%d] in the last snapshot: read the page first", a.Element)
		}
		params["ref"] = ref
		if a.Action == "type" {
			if t.selects[a.Element] {
				action = "select"
				params["option"] = a.Text
			} else {
				params["text"], params["mode"], params["pressEnter"] = a.Text, "fill", a.Enter
			}
		}
	case "key":
		if _, ok := keys[a.Key]; !ok {
			return "", fmt.Errorf("there is no key %q", a.Key)
		}
		action = "press"
		params["key"] = a.Key
		if a.Key == "Space" {
			params["key"] = " "
		}
	case "scroll":
		direction := firstOf(a.Direction, "down")
		if direction != "up" && direction != "down" {
			return "", fmt.Errorf("scroll goes down or up, not %q", direction)
		}
		params["direction"] = direction
		params["amount"] = 720 // native default viewport height
	case "back":
	default:
		return "", fmt.Errorf("Camofox does not support browser action %q", a.Action)
	}
	// An interrupted mutation may still have taken effect on the server.
	// Invalidate refs before sending; never replay an action automatically.
	t.snapshot, t.refs, t.selects = "", nil, nil
	var result map[string]any
	if _, err := b.request(ctx, http.MethodPost, camoPath(t, action), c.user, params, &result); err != nil {
		return "", err
	}
	if a.Action == "open" {
		t.url = target
	}
	if a.Action == "click" {
		if err := b.reconcileTabs(ctx, c); err != nil {
			return "", fmt.Errorf("Camofox click succeeded, but listing resulting tabs failed: %w", err)
		}
		if !slices.Contains(c.tabs, t) {
			return "Camofox click; the current tab was closed (use tabs to find remaining tabs)", nil
		}
	}
	// Unlike CDP's where(), REST has no independent title/view endpoint.
	// Returning a fresh native snapshot also makes new refs available.
	text, err := b.read(ctx, c, t, 0)
	if err != nil {
		return "", fmt.Errorf("Camofox %s succeeded, but reading the resulting page failed: %w", a.Action, err)
	}
	return "Camofox " + a.Action + "; " + text, nil
}

// reconcileTabs is scoped by our unguessable userId, not sessionKey: upstream
// registers target=_blank popups in tab groups, and canceled creates can finish
// after the client disconnects. Keep existing tab objects/order and their refs.
func (b *camoBackend) reconcileTabs(ctx context.Context, c *camoConversation) error {
	var result struct {
		Tabs []struct {
			ID  string `json:"tabId"`
			URL string `json:"url"`
		} `json:"tabs"`
	}
	if _, err := b.request(ctx, http.MethodGet, "/tabs", c.user, nil, &result); err != nil {
		return err
	}
	remote := make(map[string]string, len(result.Tabs))
	for _, tab := range result.Tabs {
		if !camoID.MatchString(tab.ID) {
			return errors.New("Camofox tabs response lacks a safe tabId")
		}
		if _, exists := remote[tab.ID]; exists {
			return errors.New("Camofox tabs response has duplicate tabId")
		}
		remote[tab.ID] = tab.URL
	}
	var current *camoTab
	if len(c.tabs) > 0 {
		current = c.tabs[c.current]
	}
	tabs := make([]*camoTab, 0, len(remote))
	for _, tab := range c.tabs {
		if u, exists := remote[tab.id]; exists {
			if tab.url != u {
				tab.snapshot, tab.refs, tab.selects = "", nil, nil
			}
			tab.url = u
			tabs = append(tabs, tab)
			delete(remote, tab.id)
		}
	}
	for _, tab := range result.Tabs {
		if _, exists := remote[tab.ID]; exists {
			tabs = append(tabs, &camoTab{id: tab.ID, url: tab.URL})
		}
	}
	c.tabs, c.current = tabs, 0
	if i := slices.Index(tabs, current); i >= 0 {
		c.current = i
	}
	return nil
}

// Native snapshots use [e12] (and older versions [link e12]). Convert only
// bracket annotations; numbers in page text are not tool element IDs.
var camoID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var camoRef = regexp.MustCompile(`\[(?:([A-Za-z]+) )?e([1-9][0-9]*)\]`)

var camoSelectLine = regexp.MustCompile(`^\s*-\s+(?:combobox|listbox|select)\b`)

// windowSnapshot in jo-inc/camofox-browser@39c8209 uses JavaScript UTF-16
// offsets. Each oversized response is a 74800-unit chunk, a marker, and a
// repeated 5000-unit tail. The final chunk overlaps that tail. Strip wrappers
// and append only the still-missing suffix, never concatenate raw windows.
func camoWindowContent(text string, offset, total int, more bool, next int) (string, error) {
	invalid := errors.New("invalid Camofox snapshot window")
	if total > 2<<20 || offset < 0 || offset >= total-5000 {
		return "", invalid
	}
	end := min(offset+74800, total)
	wantMore := offset+74800 < total-5000
	marker := "\n"
	if wantMore {
		marker = fmt.Sprintf("\n[... truncated at char %d of %d. Call snapshot with offset=%d to see more. Pagination links below. ...]\n", end, total, end)
	}
	units := utf16.Encode([]rune(text))
	mark := utf16.Encode([]rune(marker))
	n := end - offset
	if more != wantMore || (more && next != end) || len(units) != n+len(mark)+5000 {
		return "", invalid
	}
	if !slices.Equal(units[n:n+len(mark)], mark) {
		return "", invalid
	}
	content := units[:n:n]
	if !more && end < total {
		tail := units[n+len(mark):]
		content = append(content, tail[end-(total-5000):]...)
	}
	return string(utf16.Decode(content)), nil
}

func (b *camoBackend) read(ctx context.Context, c *camoConversation, t *camoTab, offset int) (string, error) {
	if offset < 0 {
		return "", errors.New("read offset must not be negative")
	}
	if offset == 0 || t.snapshot == "" {
		t.snapshot, t.refs, t.selects = "", nil, nil
		var text strings.Builder
		serverOffset := 0
		for {
			var snapshot struct {
				URL        string  `json:"url"`
				Snapshot   *string `json:"snapshot"`
				HasMore    bool    `json:"hasMore"`
				Truncated  bool    `json:"truncated"`
				NextOffset int     `json:"nextOffset"`
				TotalChars int     `json:"totalChars"`
			}
			path := camoPath(t, "snapshot")
			// request merges configured query params, but never treats path as a URL.
			_, err := b.request(ctx, http.MethodGet, path, c.user, map[string]any{"offset": serverOffset}, &snapshot)
			if err != nil {
				return "", err
			}
			if snapshot.Snapshot == nil {
				return "", errors.New("Camofox response lacks snapshot")
			}
			chunk := *snapshot.Snapshot
			if snapshot.Truncated && snapshot.TotalChars > 80000 {
				var err error
				chunk, err = camoWindowContent(chunk, serverOffset, snapshot.TotalChars, snapshot.HasMore, snapshot.NextOffset)
				if err != nil {
					return "", err
				}
			}
			text.WriteString(chunk)
			if snapshot.URL != "" {
				t.url = snapshot.URL
			}
			if text.Len() > 2<<20 {
				return "", errors.New("Camofox snapshot exceeds 2 MiB")
			}
			if !snapshot.HasMore {
				if snapshot.Truncated && snapshot.TotalChars <= 80000 {
					text.WriteString("\n[Camofox server truncated this snapshot]")
				}
				break
			}
			if snapshot.NextOffset <= serverOffset {
				return "", errors.New("Camofox snapshot pagination did not advance")
			}
			serverOffset = snapshot.NextOffset
		}
		refs := map[int]string{}
		selects := map[int]bool{}
		for _, line := range strings.Split(text.String(), "\n") {
			role := camoSelectLine.FindStringSubmatch(line)
			if role == nil {
				continue
			}
			if match := camoRef.FindStringSubmatch(line); match != nil {
				if n, err := strconv.Atoi(match[2]); err == nil {
					selects[n] = true
				}
			}
		}
		converted := camoRef.ReplaceAllStringFunc(text.String(), func(match string) string {
			parts := camoRef.FindStringSubmatch(match)
			n, err := strconv.Atoi(parts[2])
			if err != nil {
				return match
			}
			refs[n] = "e" + parts[2]
			if parts[1] != "" {
				return "[" + parts[2] + "] " + parts[1]
			}
			return "[" + parts[2] + "]"
		})
		t.snapshot, t.refs, t.selects = converted, refs, selects
	}
	text := t.snapshot
	if offset > len(text) || (offset < len(text) && !utf8.RuneStart(text[offset])) {
		return "", fmt.Errorf("invalid byte offset %d for snapshot of %d bytes", offset, len(text))
	}
	end := min(offset+maxPage, len(text))
	for end > offset && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	out := t.url + "\n\n" + text[offset:end]
	if end < len(text) {
		out += fmt.Sprintf("\n[bytes %d to %d of %d: continue with offset %d]", offset, end, len(text), end)
	}
	return out, nil
}

func (b *camoBackend) close() {
	// selectBackend holds its exclusive lock: all calls have drained. Delete
	// only this adapter's randomly namespaced users, never /stop or /start.
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	for _, c := range b.convs {
		b.request(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(c.user), c.user, nil, nil)
	}
	b.client.CloseIdleConnections()
}
