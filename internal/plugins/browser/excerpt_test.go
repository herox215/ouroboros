package browser

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPageExcerptBudgets(t *testing.T) {
	for _, provider := range []string{"cdp", "camofox"} {
		t.Run(provider, func(t *testing.T) {
			f := newFake(t)
			b := newBrowser(f)
			ctx := context.Background()
			text := "abc😀éxyz"
			f.page["read"] = map[string]any{"title": "Excerpt", "url": "https://example.com/", "text": text}
			s := settings{}
			if provider == "camofox" {
				native, server := serveCamo(t)
				native.snapshot = text
				s = camoSettings(server)
			}
			invoke := func(raw string) (string, error) { return backendCall(ctx, b, s, raw) }
			check := func(raw, want string) string {
				t.Helper()
				out, err := invoke(raw)
				if err != nil || !utf8.ValidString(out) || !strings.Contains(out, want) {
					t.Fatalf("%s: got %q, %v; want %q", raw, out, err, want)
				}
				return out
			}
			for _, raw := range []string{
				`{"action":"open","url":"https://example.com","max_bytes":4}`,
				`{"action":"open","url":"https://example.org","max_bytes":4}`,
				`{"action":"open","url":"https://example.com","new_tab":true,"max_bytes":4}`,
				`{"action":"read","max_bytes":4}`,
			} {
				out := check(raw, "abc\n[bytes 0 to 3 of 12: continue with offset 3]")
				if strings.Contains(out, "😀") {
					t.Fatalf("budget exceeded: %q", out)
				}
			}
			check(`{"action":"read","offset":3,"max_bytes":4}`, "😀\n[bytes 3 to 7 of 12: continue with offset 7]")
			check(`{"action":"read","offset":7,"max_bytes":5}`, "éxyz")
			check(`{"action":"read","offset":12,"max_bytes":4}`, "https://example.com/")
			for _, raw := range []string{`{"action":"read"}`, `{"action":"read","max_bytes":24576}`} {
				out := check(raw, text)
				if strings.Contains(out, "continue with offset") {
					t.Fatalf("unexpected truncation: %q", out)
				}
			}
			if provider == "camofox" {
				check(`{"action":"key","key":"Enter","max_bytes":4}`, "abc\n[bytes 0 to 3 of 12: continue with offset 3]")
			}
			for _, offset := range []int{-1, 4, 13} {
				if out, err := invoke(fmt.Sprintf(`{"action":"read","offset":%d,"max_bytes":4}`, offset)); err == nil {
					t.Fatalf("invalid offset %d accepted: %q", offset, out)
				}
			}
			for _, value := range []string{"-1", "0", "3", "24577", "999999999999999999999999", "4.5", `"4"`} {
				if out, err := invoke(fmt.Sprintf(`{"action":"open","url":"https://example.com","max_bytes":%s}`, value)); err == nil {
					t.Fatalf("invalid budget %s accepted: %q", value, out)
				}
			}
		})
	}
}

func TestPageBudgetDirectDefaults(t *testing.T) {
	if got, err := pageBudget(); err != nil || got != maxPage {
		t.Fatalf("default: %d, %v", got, err)
	}
	if _, err := pageBudget(4, 5); err == nil {
		t.Fatal("multiple budgets accepted")
	}
}
