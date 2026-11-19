package gui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// fakeGateway is a gateway that keeps what it was saved with, so that a test
// can tell what reached it and what was shown of it.
type fakeGateway struct {
	saved   []plugin.GatewayChange
	failure error
	fields  []plugin.GatewayField
}

func (f *fakeGateway) contribution() plugin.Gateway {
	return plugin.Gateway{
		Name:  "fake",
		Title: "Fake",
		Settings: func(context.Context) (plugin.GatewaySettings, error) {
			return plugin.GatewaySettings{Enabled: true, Homeserver: "http://127.0.0.1:8008", UserID: "@bot:test",
				AllowedUsers: []string{"@me:test"}, LoggedIn: true, Status: "Connected", Fields: f.fields}, nil
		},
		Save: func(_ context.Context, c plugin.GatewayChange) error {
			// The test looks at what the gateway got after the call: the
			// password is gone from the change by then, so keep a copy.
			c.Values = cloneValues(c.Values)
			f.saved = append(f.saved, c)
			return f.failure
		},
	}
}

func cloneValues(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func gatewayProfile(f *fakeGateway, extra ...plugin.Gateway) *profile {
	p := fakeProfile("pythia")
	list := append([]plugin.Gateway{f.contribution()}, extra...)
	p.gateways = func() []plugin.Gateway { return list }
	return p
}

// The gateways of the profile are listed with what the owner may see of them.
func TestGatewaysList(t *testing.T) {
	f := &fakeGateway{fields: []plugin.GatewayField{
		{Key: "token", Label: "Token", Kind: "secret", Value: "leaked"},
		{Key: "name", Label: "Name", Kind: "text", Value: "x", Hint: "Public hint."},
	}}
	// A gateway without settings has nothing to show.
	p := gatewayProfile(f, plugin.Gateway{Name: "none", Title: "None"})
	list, err := p.gatewayList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []GatewayInfo{{
		Name: "fake", Title: "Fake", Enabled: true, Homeserver: "http://127.0.0.1:8008", UserID: "@bot:test",
		AllowedUsers: []string{"@me:test"}, LoggedIn: true, Status: "Connected",
		Fields: []GatewayFieldInfo{{Key: "token", Label: "Token", Kind: "secret"}, {Key: "name", Label: "Name", Kind: "text", Value: "x", Hint: "Public hint."}},
	}}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("gateways %+v", list)
	}
	w := &Window{profile: p}
	if got, err := w.Gateways(); err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
}

// A window without an owner changes no gateway, and a gateway that is not
// there is no gateway.
func TestGatewaysNeedTheOwner(t *testing.T) {
	w := &Window{profile: fakeProfile("pythia")}
	if _, err := w.Gateways(); !errors.Is(err, errNoGateways) {
		t.Fatalf("%v", err)
	}
	if _, err := w.SaveGateway("fake", GatewayChangeBody{}); !errors.Is(err, errNoGateways) {
		t.Fatalf("%v", err)
	}
	f := &fakeGateway{}
	if _, err := gatewayProfile(f).saveGateway(context.Background(), "other", GatewayChangeBody{}); err == nil || len(f.saved) != 0 {
		t.Fatalf("an unknown gateway saved: %v", err)
	}
}

// Saving hands the gateway what the owner typed, and lists it anew.
func TestGatewaySave(t *testing.T) {
	f := &fakeGateway{}
	p := gatewayProfile(f)
	list, err := p.saveGateway(context.Background(), "fake", GatewayChangeBody{
		Enabled: true, Homeserver: " http://127.0.0.1:8008 ", UserID: "@bot:test", Password: "s3cret",
		AllowedUsers: []string{"@me:test"}, Values: map[string]string{"k": "v"},
	})
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v, %v", list, err)
	}
	want := plugin.GatewayChange{Enabled: true, Homeserver: "http://127.0.0.1:8008", UserID: "@bot:test", Password: "s3cret",
		AllowedUsers: []string{"@me:test"}, Values: map[string]string{"k": "v"}}
	if len(f.saved) != 1 || !reflect.DeepEqual(f.saved[0], want) {
		t.Fatalf("saved %+v", f.saved)
	}
	if _, err := p.saveGateway(context.Background(), "fake", GatewayChangeBody{Logout: true}); err != nil || !f.saved[1].Logout {
		t.Fatalf("%v %+v", err, f.saved)
	}
}

// Only a reason the gateway declares safe is shown; any other error stays
// behind the generic text, and neither carries the password.
func TestGatewaySaveErrors(t *testing.T) {
	f := &fakeGateway{failure: plugin.GatewayRefusal{Reason: "the homeserver\nrefused the login"}}
	p := gatewayProfile(f)
	_, err := p.saveGateway(context.Background(), "fake", GatewayChangeBody{Password: "s3cret"})
	if err == nil || err.Error() != "Gateway not saved: the homeserver refused the login" {
		t.Fatalf("%v", err)
	}
	f.failure = plugin.GatewayRefusal{Reason: strings.Repeat("x", 400)}
	if _, err = p.saveGateway(context.Background(), "fake", GatewayChangeBody{}); err == nil || len([]rune(err.Error())) > 200 {
		t.Fatalf("%v", err)
	}
	f.failure = errors.New("login with s3cret failed: token abc")
	_, err = p.saveGateway(context.Background(), "fake", GatewayChangeBody{Password: "s3cret"})
	if err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "abc") || !strings.Contains(err.Error(), "could not be saved") {
		t.Fatalf("%v", err)
	}
	f.failure = plugin.GatewayRefusal{}
	if _, err = p.saveGateway(context.Background(), "fake", GatewayChangeBody{}); err == nil || !strings.Contains(err.Error(), "could not be saved") {
		t.Fatalf("%v", err)
	}
	// A settings error is not repeated either.
	p.gateways = func() []plugin.Gateway {
		return []plugin.Gateway{{Name: "bad", Settings: func(context.Context) (plugin.GatewaySettings, error) {
			return plugin.GatewaySettings{}, errors.New("token abc")
		}}}
	}
	if _, err = p.gatewayList(context.Background()); err == nil || strings.Contains(err.Error(), "abc") {
		t.Fatalf("%v", err)
	}
}

// The page reaches the gateways only with the login, and a page whose node
// has no owner changes none.
func TestGatewayRoutes(t *testing.T) {
	f := &fakeGateway{}
	client, server, _ := pageOf(t, gatewayProfile(f))
	save := func(cookie *http.Cookie) *http.Response {
		req, err := http.NewRequest("POST", server.URL+"/api/gateway/save", strings.NewReader(`{"name":"fake","change":{"enabled":true,"password":"s3cret"}}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(webHeader, "1")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := do(t, client, "GET", server.URL+"/api/gateways", nil, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a login: %d", res.StatusCode)
	}
	if res := save(nil); res.StatusCode != http.StatusUnauthorized || len(f.saved) != 0 {
		t.Fatalf("saving without a login: %d", res.StatusCode)
	}
	cookie := sessionOf(t, signIn(t, client, server.URL, "owner", "correct horse battery"))
	if cookie == nil {
		t.Fatal("no login")
	}
	res := do(t, client, "GET", server.URL+"/api/gateways", cookie, nil)
	if text := body(t, res); res.StatusCode != http.StatusOK || !strings.Contains(text, `"name":"fake"`) {
		t.Fatalf("%d %s", res.StatusCode, text)
	}
	res = save(cookie)
	if text := body(t, res); res.StatusCode != http.StatusOK || strings.Contains(text, "s3cret") || len(f.saved) != 1 {
		t.Fatalf("%d %s", res.StatusCode, text)
	}

	// A page whose profile has no owner to change them through.
	client, server, _ = pageFor(t)
	cookie = sessionOf(t, signIn(t, client, server.URL, "owner", "correct horse battery"))
	res = do(t, client, "GET", server.URL+"/api/gateways", cookie, nil)
	if text := body(t, res); res.StatusCode != http.StatusConflict || !strings.Contains(text, "cannot change the gateways") {
		t.Fatalf("%d %s", res.StatusCode, text)
	}
}

func TestGatewayHandlers(t *testing.T) {
	f := &fakeGateway{}
	front := newWebFront(home.Dir(t.TempDir()), gatewayProfile(f))
	post := func(body string, page bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/gateway/save", strings.NewReader(body))
		if page {
			req.Header.Set(webHeader, "1")
		}
		rec := httptest.NewRecorder()
		front.saveGateway(rec, req)
		return rec
	}
	if rec := post(`{}`, false); rec.Code != http.StatusForbidden || len(f.saved) != 0 {
		t.Fatalf("without the page: %d", rec.Code)
	}
	if rec := post(`{"name":"fake","change":{"password":"s3cret"} nonsense`, true); rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"name":"fake","change":{"password":"`+strings.Repeat("a", 70<<10)+`"}}`, true); rec.Code != http.StatusBadRequest || len(f.saved) != 0 {
		t.Fatalf("too long: %d", rec.Code)
	}
	rec := post(`{"name":"fake","change":{"enabled":true,"homeserver":"http://127.0.0.1:8008","user_id":"@bot:test","password":"s3cret","allowed_users":["@me:test"]}}`, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "s3cret") || !strings.Contains(rec.Body.String(), `"user_id":"@bot:test"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(f.saved) != 1 || f.saved[0].Password != "s3cret" || !f.saved[0].Enabled {
		t.Fatalf("saved %+v", f.saved)
	}
	f.failure = errors.New("password s3cret rejected")
	if rec := post(`{"name":"fake","change":{"password":"s3cret"}}`, true); rec.Code != http.StatusConflict || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	f.failure = nil
	if rec := post(`{"name":"nope","change":{}}`, true); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	get := httptest.NewRecorder()
	front.gateways(get, httptest.NewRequest("GET", "/api/gateways", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"name":"fake"`) {
		t.Fatalf("%d %s", get.Code, get.Body)
	}
}
