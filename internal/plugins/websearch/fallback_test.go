package websearch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// answering is an engine of these tests: it answers with hits, or with err, and
// never reaches the network.
func answering(name string, hits []hit, err error) engine {
	return engine{Name: name, Ask: func(context.Context, *http.Client, string, int) ([]hit, error) {
		return hits, err
	}}
}

// ddgPage is a page of DuckDuckGo as its html endpoint answers it: two results
// with an excerpt, one of them through the redirect of DuckDuckGo, and one
// without.
const ddgPage = `<html><body>
<div class="result results_links results_links_deep web-result ">
  <div class="links_main links_deep result__body">
    <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://example.com/a">A &amp; B</a></h2>
    <div class="result__extras"><a class="result__url" href="https://example.com/a">example.com</a></div>
    <a class="result__snippet" href="https://example.com/a">The <b>answer</b>&nbsp;is 42.</a>
  </div>
</div>
<div class="result results_links results_links_deep web-result ">
  <div class="links_main links_deep result__body">
    <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb&amp;rut=abc">B</a></h2>
    <a class="result__snippet" href="https://example.org/b">Through the redirect</a>
  </div>
</div>
<div class="result results_links results_links_deep web-result ">
  <div class="links_main links_deep result__body">
    <h2 class="result__title"><a rel="nofollow" class="result__a" href="https://example.net/c?x=1&amp;y=2">C</a></h2>
  </div>
</div>
</body></html>`

// braveSample is a page of Brave as it answers it: two results, the first with the
// title and the excerpt as Brave writes them.
const braveSample = `<html><body>
<div class="snippet svelte-jmfu5f" data-pos="1" data-type="web" data-keynav="true"><div class="result-body"><div class="result-content"><a href="https://en.wikipedia.org/wiki/Ouroboros" target="_self" class="svelte-14r20fy l1"><div class="site-name-content"><div class="url-wrapper"><cite class="snippet-url">en.wikipedia.org</cite></div></div><div class="title search-snippet-title line-clamp-1 svelte-14r20fy" title="Ouroboros - Wikipedia">Ouroboros - Wikipedia</div></a><div class="generic-snippet svelte-1cwdgg3"><div class="content desktop-default-regular t-primary line-clamp-dynamic svelte-1cwdgg3"><!--[0--><span class="t-secondary">6 days ago -</span><!-- -->The <strong>ouroboros</strong> is a snake&nbsp;eating its tail.<!----></div></div></div></div></div>
<div class="snippet svelte-jmfu5f" data-pos="2" data-type="web"><div class="result-body"><div class="result-content"><a href="https://example.org/2?a=1&amp;b=2" class="svelte-14r20fy l1"><div class="title svelte-14r20fy" title="Second">Second</div></a><div class="generic-snippet"><div class="content t-primary">Nothing <em>else</em>.</div></div></div></div></div>
</body></html>`

func TestReadResults(t *testing.T) {
	ddg := duckDuckGoHits(ddgPage, 10)
	want := []hit{
		{Title: "A & B", URL: "https://example.com/a", Excerpt: "The answer is 42."},
		{Title: "B", URL: "https://example.org/b", Excerpt: "Through the redirect"},
		{Title: "C", URL: "https://example.net/c?x=1&y=2"},
	}
	if len(ddg) != len(want) {
		t.Fatalf("read %d results, want %d: %+v", len(ddg), len(want), ddg)
	}
	for i, h := range ddg {
		if h != want[i] {
			t.Errorf("result %d: %+v, want %+v", i, h, want[i])
		}
	}
	if got := duckDuckGoHits(ddgPage, 2); len(got) != 2 || got[1].Title != "B" {
		t.Errorf("limit: %+v", got)
	}

	brave := braveHits(braveSample, 10)
	wantBrave := []hit{
		{Title: "Ouroboros - Wikipedia", URL: "https://en.wikipedia.org/wiki/Ouroboros", Excerpt: "6 days ago - The ouroboros is a snake eating its tail."},
		{Title: "Second", URL: "https://example.org/2?a=1&b=2", Excerpt: "Nothing else."},
	}
	if len(brave) != len(wantBrave) {
		t.Fatalf("read %d results, want %d: %+v", len(brave), len(wantBrave), brave)
	}
	for i, h := range brave {
		if h != wantBrave[i] {
			t.Errorf("result %d: %+v, want %+v", i, h, wantBrave[i])
		}
	}
}

func TestReadResultsOfAnErrorPage(t *testing.T) {
	// A captcha or an error page has no result in it, and says nothing of one.
	page := `<html><body><h1>Making sure you're not a bot!</h1><p>Please verify.</p></body></html>`
	if hits := duckDuckGoHits(page, 10); hits != nil {
		t.Errorf("DuckDuckGo: %+v", hits)
	}
	if hits := braveHits(page, 10); hits != nil {
		t.Errorf("Brave: %+v", hits)
	}
}

func TestFallbackAsksTheEnginesInOrder(t *testing.T) {
	old := engines
	t.Cleanup(func() { engines = old })
	var asked []string
	with := func(name string, hits []hit, err error) engine {
		return engine{Name: name, Ask: func(context.Context, *http.Client, string, int) ([]hit, error) {
			asked = append(asked, name)
			return hits, err
		}}
	}
	engines = []engine{
		with("first", nil, errors.New("HTTP 202")),
		with("second", []hit{{Title: "T", URL: "https://example.com"}}, nil),
		with("third", nil, errors.New("HTTP 429")),
	}
	name, text, err := fallback(context.Background(), http.DefaultClient, "ouro", 5)
	if err != nil || name != "second" || !strings.Contains(text, "URL: https://example.com") {
		t.Fatalf("%q, %q, %v", name, text, err)
	}
	if strings.Join(asked, ",") != "first,second" {
		t.Errorf("asked %v: an engine after one that answered is not asked", asked)
	}

	// One that found nothing is passed over like one that failed.
	engines = []engine{with("first", nil, nil), with("second", []hit{{Title: "T", URL: "https://example.com"}}, nil)}
	if name, _, err := fallback(context.Background(), http.DefaultClient, "ouro", 5); err != nil || name != "second" {
		t.Fatalf("%q, %v", name, err)
	}

	engines = []engine{with("first", nil, errors.New("HTTP 202")), with("second", nil, nil)}
	if _, _, err := fallback(context.Background(), http.DefaultClient, "ouro", 5); err == nil ||
		!strings.Contains(err.Error(), "HTTP 202") || !strings.Contains(err.Error(), "second: no results") {
		t.Fatalf("%v", err)
	}
}

func TestDuckDuckGoAsksForItsPageFirst(t *testing.T) {
	oldHome, oldPage := duckDuckGoHome, duckDuckGoPage
	t.Cleanup(func() { duckDuckGoHome, duckDuckGoPage = oldHome, oldPage })
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != agent {
			t.Errorf("user agent: %q", r.Header.Get("User-Agent"))
		}
		switch r.Method {
		case http.MethodGet:
			asked = append(asked, "page")
			http.SetCookie(w, &http.Cookie{Name: "probe", Value: "1", Path: "/"})
			w.Write([]byte(`<html><body>search</body></html>`))
		case http.MethodPost:
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
				r.Header.Get("Referer") == "" || r.Header.Get("Origin") == "" {
				t.Errorf("form request: %v", r.Header)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			asked = append(asked, r.PostForm.Get("q"))
			if _, err := r.Cookie("probe"); err != nil {
				t.Errorf("the result page was asked without the cookie of the search page: %v", err)
			}
			w.Write([]byte(ddgPage))
		}
	}))
	defer server.Close()
	duckDuckGoHome, duckDuckGoPage = server.URL+"/", server.URL+"/html/"

	hits, err := duckDuckGo(context.Background(), server.Client(), "ouro pur", 5)
	if err != nil || len(hits) != 3 {
		t.Fatalf("%+v, %v", hits, err)
	}
	if strings.Join(asked, ",") != "page,ouro pur" {
		t.Errorf("asked %v", asked)
	}
}

func TestBraveAsksItsResultPage(t *testing.T) {
	old := bravePage
	t.Cleanup(func() { bravePage = old })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.RawQuery, "q=") {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		w.Write([]byte(braveSample))
	}))
	defer server.Close()
	bravePage = server.URL + "/search"

	hits, err := brave(context.Background(), server.Client(), "ouro", 5)
	if err != nil || len(hits) != 2 || hits[0].URL != "https://en.wikipedia.org/wiki/Ouroboros" {
		t.Fatalf("%+v, %v", hits, err)
	}
}

func TestEngineFailure(t *testing.T) {
	old := bravePage
	t.Cleanup(func() { bravePage = old })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	bravePage = server.URL + "/search"

	if _, err := brave(context.Background(), server.Client(), "ouro", 5); err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("%v", err)
	}
}

func TestSearchFallsBackWhenExaDoesNotAnswer(t *testing.T) {
	old := engines
	t.Cleanup(func() { engines = old })
	engines = []engine{answering("DuckDuckGo", []hit{{Title: "Ouroboros", URL: "https://example.com/o", Excerpt: "a snake"}}, nil)}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	defer server.Close()
	s := &search{client: server.Client(), endpoint: server.URL}
	out, err := s.run(context.Background(), args{Query: "ouro"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Exa did not answer", "rate limit of Exa", "DuckDuckGo", "URL: https://example.com/o", "a snake"} {
		if !strings.Contains(out, want) {
			t.Errorf("the answer does not say %q: %s", want, out)
		}
	}
}

func TestSearchFailsWhenNothingAnswers(t *testing.T) {
	old := engines
	t.Cleanup(func() { engines = old })
	engines = []engine{answering("DuckDuckGo", nil, errors.New("HTTP 202"))}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
	}))
	defer server.Close()
	s := &search{client: server.Client(), endpoint: server.URL}
	_, err := s.run(context.Background(), args{Query: "ouro"})
	if err == nil || !strings.Contains(err.Error(), "rate limit of Exa") || !strings.Contains(err.Error(), "DuckDuckGo: HTTP 202") {
		t.Fatalf("%v", err)
	}
}

// What the fallback found is written one result after the other, with an
// empty line between them and the excerpt only where there is one.
func TestHitsText(t *testing.T) {
	got := hitsText([]hit{{Title: "A", URL: "https://example.com/a", Excerpt: "first"}, {Title: "B", URL: "https://example.com/b"}})
	want := "\nTitle: A\nURL: https://example.com/a\nExcerpt: first\n\nTitle: B\nURL: https://example.com/b"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A result page that does not come, breaks off or is too large is no answer.
func TestPageFailures(t *testing.T) {
	answer := func(body io.Reader, err error) *http.Client {
		return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if err != nil {
				return nil, err
			}
			if r.Header.Get("User-Agent") != agent || r.Header.Get("Accept-Language") != "en-US,en;q=0.9" {
				t.Errorf("request: %v", r.Header)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(body), Request: r}, nil
		})}
	}
	for _, tc := range []struct {
		name   string
		client *http.Client
		want   string
	}{
		{"unreachable", answer(nil, errors.New("no route to host")), "no route to host"},
		{"broken", breaking(), "connection reset"},
		{"large", answer(strings.NewReader(strings.Repeat("x", maxFallback+1)), nil), "the result page is larger than 4 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "http://engine.test/", nil)
			if body, err := page(tc.client, req); err == nil || !strings.Contains(err.Error(), tc.want) || body != "" {
				t.Fatalf("%d bytes, %v", len(body), err)
			}
		})
	}
	req, _ := http.NewRequest(http.MethodGet, "http://engine.test/", nil)
	if body, err := page(answer(strings.NewReader(strings.Repeat("x", maxFallback)), nil), req); err != nil || len(body) != maxFallback {
		t.Fatalf("a page of exactly 4 MiB: %d bytes, %v", len(body), err)
	}
}

// DuckDuckGo fails at the step that fails, and says which one it was.
func TestDuckDuckGoFailures(t *testing.T) {
	oldHome, oldPage := duckDuckGoHome, duckDuckGoPage
	t.Cleanup(func() { duckDuckGoHome, duckDuckGoPage = oldHome, oldPage })
	var failing string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == failing {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(ddgPage))
	}))
	defer server.Close()
	client := server.Client()
	for _, tc := range []struct{ name, home, page, failing, want string }{
		{"home is no URL", "http://[::1", server.URL + "/html/", "", "missing ']' in host"},
		{"home fails", server.URL + "/", server.URL + "/html/", http.MethodGet, "the search page of DuckDuckGo: HTTP 503"},
		{"results are no URL", server.URL + "/", "http://[::1", "", "missing ']' in host"},
		{"results fail", server.URL + "/", server.URL + "/html/", http.MethodPost, "the search of DuckDuckGo: HTTP 503"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			duckDuckGoHome, duckDuckGoPage, failing = tc.home, tc.page, tc.failing
			if hits, err := duckDuckGo(context.Background(), client, "ouro", 5); err == nil || !strings.Contains(err.Error(), tc.want) || hits != nil {
				t.Fatalf("%+v, %v", hits, err)
			}
		})
	}
	if client.Jar != nil {
		t.Fatal("the cookies of DuckDuckGo went into the client of the node")
	}
}

// A result page of Brave that is no URL is asked of nobody.
func TestBraveRequestFailure(t *testing.T) {
	old := bravePage
	t.Cleanup(func() { bravePage = old })
	bravePage = "http://[::1"
	if hits, err := brave(context.Background(), http.DefaultClient, "ouro", 5); err == nil || !strings.Contains(err.Error(), "missing ']' in host") || hits != nil {
		t.Fatalf("%+v, %v", hits, err)
	}
}

// A block of a result page without a link, without a title or with a link
// that leads nowhere is no result, and no more results are read than asked
// for.
func TestReadResultsSkipsWhatIsNoResult(t *testing.T) {
	ddg := `<html><body>
<div class="result results_links"><a class="result__url" href="https://example.com/url">only the address</a></div>
<div class="result results_links"><a class="result__a" href="https://example.com/image"><img src="a.png"></a></div>
<div class="result results_links"><a class="result__a" href="http://[::1">Broken</a></div>
<div class="result results_links"><a class="result__a" href="//duckduckgo.com/l/?rut=abc">Nowhere</a></div>
<div class="result results_links"><a class="result__a" href="https://example.com/one">One</a></div>
<div class="result results_links"><a class="result__a" href="https://example.com/two">Two</a></div>
</body></html>`
	if got := duckDuckGoHits(ddg, 10); len(got) != 2 || got[0] != (hit{Title: "One", URL: "https://example.com/one"}) || got[1].Title != "Two" {
		t.Errorf("DuckDuckGo: %+v", got)
	}
	brave := `<html><body>
<div class="snippet" data-type="web"><div class="title">No link</div></div>
<div class="snippet" data-type="web"><a href="https://example.com/untitled">untitled</a></div>
<div class="snippet" data-type="web"><a href="https://example.com/image"><div class="title"><img src="a.png"></div></a></div>
<div class="snippet" data-type="web"><a href="https://example.com/one"><div class="title">One</div></a></div>
<div class="snippet" data-type="web"><a href="https://example.com/two"><div class="title">Two</div></a></div>
</body></html>`
	if got := braveHits(brave, 10); len(got) != 2 || got[0] != (hit{Title: "One", URL: "https://example.com/one"}) || got[1].Title != "Two" {
		t.Errorf("Brave: %+v", got)
	}
	if got := braveHits(brave, 1); len(got) != 1 || got[0].Title != "One" {
		t.Errorf("Brave, limit 1: %+v", got)
	}
}

// The address of a result is the one it leads to: through the redirect of
// DuckDuckGo, and none of a link that is no URL.
func TestUnredirect(t *testing.T) {
	for link, want := range map[string]string{
		"https://example.com/a?x=1&amp;y=2":                             "https://example.com/a?x=1&y=2",
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org%2Fb":        "https://example.org/b",
		"https://html.duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.org": "https://example.org",
		"https://duckduckgo.com/about":                                  "https://duckduckgo.com/about",
		"https://example.com/l/?uddg=https%3A%2F%2Fexample.org":         "https://example.com/l/?uddg=https%3A%2F%2Fexample.org",
		"http://[::1": "",
	} {
		if got := unredirect(link); got != want {
			t.Errorf("%s: %q, want %q", link, got, want)
		}
	}
}
