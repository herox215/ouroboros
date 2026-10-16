package websearch

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
)

// The endpoint of Exa is free and rate-limited, and when it does not answer,
// the search is asked of a search engine whose result page needs no key either:
// see engines. The page is read as a browser shows it, so a fallback returns
// less than Exa, a handful of results with short excerpts. An engine that
// changed its page or that blocks the node answers nothing, and the next one is
// asked; only when none of them finds anything does the search fail.

// hit is one result of a fallback search.
type hit struct {
	Title   string
	URL     string
	Excerpt string
}

// engine is a search engine that answers without a key.
type engine struct {
	// Name is how the answer says which engine found the results.
	Name string
	// Ask asks the engine for query and returns what it found, at most limit.
	Ask func(ctx context.Context, c *http.Client, query string, limit int) ([]hit, error)
}

// engines are asked in this order. DuckDuckGo answers the plain request of a
// node once it was asked for its page first, Brave answers without that, and
// both answered when this was written; another node, or another day, needs the
// next engine, which is why there is more than one.
//
// Tests replace them.
var engines = []engine{
	{"DuckDuckGo", duckDuckGo},
	{"Brave", brave},
}

// The pages the fallback reads; tests replace them.
var (
	duckDuckGoHome = "https://duckduckgo.com/"
	duckDuckGoPage = "https://html.duckduckgo.com/html/"
	bravePage      = "https://search.brave.com/search"
)

// agent is what the fallback says of itself: the engines answer a client that
// asks as a browser more willingly than one that names itself.
const agent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// maxFallback is how much of a result page is read. A result page that is
// larger is not one, and what is read of it is left alone.
const maxFallback = 4 << 20

// fallback asks the engines, one after the other, and returns which of them
// answered and what it found. It fails only when none of them found anything,
// and says then what each of them answered.
func fallback(ctx context.Context, c *http.Client, query string, limit int) (string, string, error) {
	var missed []string
	for _, e := range engines {
		hits, err := e.Ask(ctx, c, query, limit)
		if err != nil {
			missed = append(missed, e.Name+": "+err.Error())
			continue
		}
		if len(hits) == 0 {
			missed = append(missed, e.Name+": no results")
			continue
		}
		return e.Name, hitsText(hits), nil
	}
	return "", "", fmt.Errorf("the fallback search found nothing (%s)", strings.Join(missed, "; "))
}

// hitsText writes what a fallback found as the tool returns it.
func hitsText(hits []hit) string {
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("\nTitle: " + h.Title + "\nURL: " + h.URL)
		if h.Excerpt != "" {
			b.WriteString("\nExcerpt: " + h.Excerpt)
		}
	}
	return bounded(b.String())
}

// page reads what a request answers: its body as text, and an error for a
// status that is not 200, so that the next engine is asked instead of reading
// an error page.
func page(c *http.Client, req *http.Request) (string, error) {
	req.Header.Set("User-Agent", agent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFallback+1))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if len(data) > maxFallback {
		return "", errors.New("the result page is larger than 4 MiB")
	}
	return string(data), nil
}

// duckDuckGo asks the html endpoint of DuckDuckGo, which answers its result
// page only to a client that asked for the search page before it and brings the
// cookie from there with it.
func duckDuckGo(ctx context.Context, c *http.Client, query string, limit int) ([]hit, error) {
	if c.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		clone := *c // the client of the node stays as it is, and its limits hold
		clone.Jar = jar
		c = &clone
	}
	home, err := http.NewRequestWithContext(ctx, http.MethodGet, duckDuckGoHome, nil)
	if err != nil {
		return nil, err
	}
	if _, err := page(c, home); err != nil {
		return nil, fmt.Errorf("the search page of DuckDuckGo: %w", err)
	}
	form := url.Values{"q": {query}, "kl": {"wt-wt"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, duckDuckGoPage, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", duckDuckGoHome)
	req.Header.Set("Origin", strings.TrimSuffix(duckDuckGoHome, "/"))
	body, err := page(c, req)
	if err != nil {
		return nil, fmt.Errorf("the search of DuckDuckGo: %w", err)
	}
	return duckDuckGoHits(body, limit), nil
}

// brave asks the result page of Brave, which needs no cookie.
func brave(ctx context.Context, c *http.Client, query string, limit int) ([]hit, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bravePage+"?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	body, err := page(c, req)
	if err != nil {
		return nil, fmt.Errorf("the search of Brave: %w", err)
	}
	return braveHits(body, limit), nil
}

// The parts of a result page of DuckDuckGo: every result starts a block, and in
// it are the link with its title and the link with the excerpt.
var (
	ddgBlock = regexp.MustCompile(`<div class="result results_links`)
	ddgLink  = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	ddgText  = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)
)

// duckDuckGoHits reads the results of a page of DuckDuckGo, at most limit.
func duckDuckGoHits(body string, limit int) []hit {
	blocks := ddgBlock.Split(body, -1)
	var hits []hit
	for _, block := range blocks[1:] { // what is before the first result is the page itself
		link := ddgLink.FindStringSubmatch(block)
		if link == nil {
			continue
		}
		h := hit{Title: markup(link[2]), URL: unredirect(link[1])}
		if h.Title == "" || h.URL == "" {
			continue
		}
		if text := ddgText.FindStringSubmatch(block); text != nil {
			h.Excerpt = markup(text[1])
		}
		if hits = append(hits, h); len(hits) == limit {
			break
		}
	}
	return hits
}

// The parts of a result page of Brave: every result starts a block, and in it
// are the link of the result, its title and its excerpt.
var (
	braveBlock = regexp.MustCompile(`<div class="snippet[^"]*"[^>]*data-type="web"`)
	braveLink  = regexp.MustCompile(`href="(https?://[^"]+)"`)
	braveTitle = regexp.MustCompile(`(?s)<div class="title[^"]*"[^>]*>(.*?)</div>`)
	braveText  = regexp.MustCompile(`(?s)<div class="content[^"]*"[^>]*>(.*?)</div>`)
)

// braveHits reads the results of a page of Brave, at most limit.
func braveHits(body string, limit int) []hit {
	blocks := braveBlock.Split(body, -1)
	var hits []hit
	for _, block := range blocks[1:] {
		link := braveLink.FindStringSubmatch(block)
		title := braveTitle.FindStringSubmatch(block)
		if link == nil || title == nil {
			continue
		}
		h := hit{Title: markup(title[1]), URL: html.UnescapeString(link[1])}
		if h.Title == "" || h.URL == "" {
			continue
		}
		if text := braveText.FindStringSubmatch(block); text != nil {
			h.Excerpt = markup(text[1])
		}
		if hits = append(hits, h); len(hits) == limit {
			break
		}
	}
	return hits
}

// The markup of a page: its comments, the tags that stand on a line of their
// own, which separate what they hold, and the rest of the tags, which a browser
// shows no space for.
var (
	comment = regexp.MustCompile(`(?s)<!--.*?-->`)
	block   = regexp.MustCompile(`(?is)</?(?:div|p|li|ul|ol|tr|td|th|table|section|main|header|footer|h[1-6]|br|cite)\b[^>]*>`)
	tag     = regexp.MustCompile(`(?s)<[^>]*>`)
)

// markup is what a piece of a result page says: without its comments and its
// tags, with the entities of the page read, and with its space as one space. A
// fragment with no text in it, as an image has, says nothing.
func markup(fragment string) string {
	fragment = comment.ReplaceAllString(fragment, " ")
	fragment = block.ReplaceAllString(fragment, " ")
	fragment = tag.ReplaceAllString(fragment, "")
	return strings.Join(strings.Fields(html.UnescapeString(fragment)), " ")
}

// unredirect is the address a result of DuckDuckGo leads to: with the entities
// of the page read, and, for a result DuckDuckGo hands out through a page of
// its own, the address that page holds in uddg.
func unredirect(link string) string {
	link = html.UnescapeString(link)
	if strings.HasPrefix(link, "//") {
		link = "https:" + link
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	if !strings.HasSuffix(u.Host, "duckduckgo.com") || u.Path != "/l/" {
		return link
	}
	return u.Query().Get("uddg")
}
