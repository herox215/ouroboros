package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
)

// Only the launch-time blank page is eligible; ambiguous pages and targets
// in a peer context or opened by another page are left alone.
func TestLaunchStartupTarget(t *testing.T) {
	for _, tc := range []struct{ name, targets, want string }{
		{"blank", `[{"targetId":"startup","type":"page","url":"about:blank"}]`, "startup"},
		{"none", `[]`, ""},
		{"default context ID", `[{"targetId":"startup","type":"page","url":"about:blank","browserContextId":"default"}]`, "startup"},
		{"other targets", `[{"targetId":"worker","type":"service_worker","url":"about:blank"},{"targetId":"peer","type":"page","url":"about:blank","browserContextId":"peer"},{"targetId":"popup","type":"page","url":"about:blank","openerId":"other"},{"targetId":"page","type":"page","url":"https://example.com"},{"targetId":"startup","type":"page","url":"about:blank"}]`, "startup"},
		{"ambiguous", `[{"targetId":"one","type":"page","url":"about:blank"},{"targetId":"two","type":"page","url":"about:blank"}]`, ""},
		{"ambiguous of three", `[{"targetId":"one","type":"page","url":"about:blank"},{"targetId":"two","type":"page","url":"about:blank"},{"targetId":"three","type":"page","url":"about:blank"}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := strings.Replace(answering, "    *) printf", `    *Target.getBrowserContexts*) printf '{"id":%s,"result":{"browserContextIds":["peer"]}}\0' "$id" >&4;;
    *Target.getTargets*) printf '{"id":%s,"result":{"targetInfos":%s}}\0' "$id" '`+tc.targets+`' >&4;;
    *) printf`, 1)
			program(t, script)
			p, _, err := run(context.Background(), programs[""][1], t.TempDir(), false, "")
			if err != nil {
				t.Fatal(err)
			}
			defer p.end()
			if p.startup != tc.want {
				t.Fatalf("startup %q, want %q", p.startup, tc.want)
			}
		})
	}
}

// A peer may start the process, but only the first owner adopts its launch
// tab, including its sessions and event listeners. Other conversations
// cannot list, switch to, or close it.
func TestStartupTabOwnership(t *testing.T) {
	f := newFake(t)
	start = func(ctx context.Context, profile string, visible bool) (*process, error) {
		p, err := f.start(ctx, profile, visible)
		if err == nil {
			p.startup = "startup"
		}
		return p, err
	}
	b := newBrowser(f)
	ctx := context.Background()
	peer := a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "peer"})
	got, err := call(peer, b, settings{}, `{"action":"tabs"}`)
	is(t, "peer", got, err, "the conversation has no tab")
	f.on("Target.getTargetInfo", func(json.RawMessage) (any, error) {
		return map[string]any{"targetInfo": map[string]string{"title": "", "url": "about:blank"}}, nil
	})
	got, err = call(ctx, b, settings{}, `{"action":"tabs"}`)
	is(t, "startup", got, err, `[1] "" about:blank (current)`)
	if f.last("Target.attachToTarget") != `{"flatten":true,"targetId":"startup"}` || f.last("Target.createTarget") != "" {
		t.Fatalf("attached %s; created %s", f.last("Target.attachToTarget"), f.last("Target.createTarget"))
	}
	if _, err := call(ctx, b, settings{}, `{"action":"read"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, b, settings{}, `{"action":"open","url":"example.com"}`); err != nil {
		t.Fatal(err)
	}
	if f.last("Target.createTarget") != "" {
		t.Fatal("open did not reuse startup")
	}
	for _, domain := range []string{"Page.enable", "Runtime.enable", "Log.enable"} {
		if !strings.Contains(strings.Join(f.got(), " "), domain) {
			t.Fatalf("startup missing %s", domain)
		}
	}
	// Restart reuses the current saved tab instead of adding a stray blank.
	b.proc.end()
	if _, err := call(ctx, b, settings{}, `{"action":"read"}`); err != nil {
		t.Fatal(err)
	}
	if len(b.convs["/"].tabs) != 1 || f.last("Target.createTarget") != "" || f.last("Page.navigate") != `{"url":"https://example.com/"}` {
		t.Fatal("replacement did not restore the saved tab in startup")
	}
	c, p, err := b.conversation(ctx, "other", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err = c.act(ctx, p, "", args{Action: "tabs"})
	is(t, "other", got, err, "the conversation has no tab")
	_, err = c.act(ctx, p, "", args{Action: "switch", Tab: 1})
	if err == nil {
		t.Fatal("other conversation switched to startup")
	}
	c.mu.Unlock()
	got, err = b.closeTab("other")
	is(t, "other close", got, err, "there is no tab to close")
	got, err = call(ctx, b, settings{}, `{"action":"close"}`)
	is(t, "close startup", got, err, "closed the tab")
	if f.last("Target.closeTarget") != `{"targetId":"startup"}` {
		t.Fatal("startup not closed")
	}
	got, err = call(ctx, b, settings{}, `{"action":"tabs"}`)
	is(t, "no readoption", got, err, "the conversation has no tab")
	// Each replacement process has a new launch tab to adopt.
	b.proc.end()
	got, err = call(ctx, b, settings{}, `{"action":"tabs"}`)
	is(t, "replacement", got, err, `[1] "" about:blank (current)`)
}

func TestStartupClosedBeforeAdoption(t *testing.T) {
	f := newFake(t)
	start = func(ctx context.Context, profile string, visible bool) (*process, error) {
		p, err := f.start(ctx, profile, visible)
		if err == nil {
			p.startup = "startup"
		}
		return p, err
	}
	f.on("Target.attachToTarget", func(raw json.RawMessage) (any, error) {
		var params struct {
			Target string `json:"targetId"`
		}
		json.Unmarshal(raw, &params)
		if params.Target == "startup" {
			return nil, &cdpError{Message: "No target with given id found"}
		}
		return map[string]string{"sessionId": "replacement"}, nil
	})
	b := newBrowser(f)
	if _, err := call(context.Background(), b, settings{}, `{"action":"read"}`); err != nil {
		t.Fatal(err)
	}
	if current(b).target != "T" {
		t.Fatal("did not replace the closed startup tab")
	}
}

func TestRealStartupTab(t *testing.T) {
	if _, err := find(); err != nil || testing.Short() {
		t.Skip("no browser:", err)
	}
	b := &browser{profile: t.TempDir(), convs: map[string]*conversation{}}
	t.Cleanup(b.close)
	ctx := context.Background()
	got, err := call(ctx, b, settings{}, `{"action":"tabs"}`)
	if err != nil || !strings.Contains(got, "about:blank") {
		t.Fatalf("startup: %q, %v", got, err)
	}
	if _, err := call(ctx, b, settings{}, `{"action":"close"}`); err != nil {
		t.Fatal(err)
	}
	// The browser answers closeTarget before the page is gone.
	for deadline := time.Now().Add(5 * time.Second); pages(t, b) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("startup page still open")
		}
	}
}

// pages counts the pages open in the browser of b.
func pages(t *testing.T, b *browser) int {
	t.Helper()
	var targets struct {
		Infos []struct {
			Type string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := b.proc.call(context.Background(), "", "Target.getTargets", nil, &targets); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, target := range targets.Infos {
		if target.Type == "page" {
			n++
		}
	}
	return n
}

func TestStartupAttachmentRetry(t *testing.T) {
	f := newFake(t)
	start = func(ctx context.Context, profile string, visible bool) (*process, error) {
		p, err := f.start(ctx, profile, visible)
		if err == nil {
			p.startup = "startup"
		}
		return p, err
	}
	b := newBrowser(f)
	ctx := context.Background()
	f.fail("Runtime.enable", "refused")
	_, err := call(ctx, b, settings{}, `{"action":"tabs"}`)
	fails(t, "attach", err, "Runtime.enable: refused")
	c, p, err := b.conversation(ctx, "other", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.tabs) != 0 {
		t.Fatal("failed adoption released ownership")
	}
	c.mu.Unlock()
	f.on("Runtime.enable", func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	if _, err := call(ctx, b, settings{}, `{"action":"read"}`); err != nil {
		t.Fatal(err)
	}
	if b.convs["/"].tabs[0].proc != p || f.last("Target.createTarget") != "" {
		t.Fatal("retry did not attach startup")
	}
}
