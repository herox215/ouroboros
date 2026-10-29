package deepseek_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/openai"
	"ouroboros/internal/plugins/deepseek"
	"ouroboros/plugin"
)

// balanceOf asks the provider for the balance of an access whose API answers
// GET /user/balance with status and body.
func balanceOf(t *testing.T, status int, body string) (plugin.Usage, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/balance" || r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("User-Agent") != openai.UserAgent {
			t.Errorf("balance request: %s %s, headers %v", r.Method, r.URL.Path, r.Header)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	// The saved endpoint wins over the provider option.
	p := deepseek.Provider(deepseek.Options{BaseURL: "http://unused.invalid"})
	return p.Usage(t.Context(), access{openai.Credentials(srv.URL+"/", key)})
}

// The total of every currency is reported as DeepSeek tells it, and so is a
// balance that no longer allows calls, also with no currency listed.
func TestBalance(t *testing.T) {
	got, err := balanceOf(t, http.StatusOK, `{"is_available":true,"balance_infos":[`+
		`{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"},`+
		`{"currency":"USD","total_balance":"-0.5","granted_balance":"0.00","topped_up_balance":"-0.5"}]}`)
	want := plugin.Usage{Balances: []plugin.UsageBalance{{Currency: "CNY", Amount: "110.00"}, {Currency: "USD", Amount: "-0.5"}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("balance %+v, %v", got, err)
	}
	got, err = balanceOf(t, http.StatusOK, `{"is_available":false,"balance_infos":[]}`)
	if err != nil || !reflect.DeepEqual(got, plugin.Usage{Insufficient: true}) {
		t.Fatalf("insufficient %+v, %v", got, err)
	}
}

// An answer that is not as documented reports no balance, and an error that
// shows neither the answer nor the key.
func TestBalanceRejectsMalformedData(t *testing.T) {
	info := func(currency, total string) string {
		return `{"is_available":true,"balance_infos":[{"currency":` + currency + `,"total_balance":` + total + `}]}`
	}
	for _, body := range []string{
		"", "null", "{}", `{"is_available":true}`, `{"balance_infos":[]}`, `{"is_available":null,"balance_infos":[]}`,
		`{"is_available":"yes","balance_infos":[]}`, `{"is_available":true,"balance_infos":null}`,
		`{"is_available":true,"balance_infos":[]} {}`,
		info(`"USD"`, `null`), info(`null`, `"1.00"`), info(`"usd"`, `"1.00"`), info(`"US"`, `"1.00"`),
		info(`"USD"`, `1.5`), info(`"USD"`, `"1e3"`), info(`"USD"`, `".5"`), info(`"USD"`, `"1."`),
		info(`"USD"`, `"\u001b[31m`+key+`"`), info(`"USD"`, `"1.00 `+key+`"`),
	} {
		got, err := balanceOf(t, http.StatusOK, body)
		if err == nil || !reflect.DeepEqual(got, plugin.Usage{}) || strings.Contains(err.Error(), key) || body != "" && strings.Contains(err.Error(), body) {
			t.Fatalf("%s: accepted or echoed: %+v, %v", body, got, err)
		}
	}
}

// A key that is refused is told as that; neither the answer nor the
// credentials show in an error.
func TestBalanceErrors(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		got, err := balanceOf(t, status, `{"error":{"message":"`+key+`"}}`)
		if err == nil || !reflect.DeepEqual(got, plugin.Usage{}) || strings.Contains(err.Error(), key) {
			t.Fatalf("HTTP %d: %+v, %v", status, got, err)
		}
		if errors.Is(err, plugin.ErrKeyRejected) != (status != 500) {
			t.Fatalf("HTTP %d: %v", status, err)
		}
	}
	p := deepseek.Provider(deepseek.Options{})
	for _, a := range []plugin.Access{unreadable{}, access{plugin.Credentials{Data: []byte("{")}}} {
		if got, err := p.Usage(t.Context(), a); err == nil || !reflect.DeepEqual(got, plugin.Usage{}) || err.Error() != "cannot read DeepSeek balance credentials" {
			t.Fatalf("credentials: %+v, %v", got, err)
		}
	}
}

// unreadable is an access whose credentials cannot be read.
type unreadable struct{}

func (unreadable) Credentials() (plugin.Credentials, error) {
	return plugin.Credentials{}, errors.New("unreadable " + key)
}
func (unreadable) Renew(func(*plugin.Credentials) error) error { return nil }
