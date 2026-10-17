package browser

import (
	"os"
	"strings"
	"testing"
)

func TestBrowserTokenEnv(t *testing.T) {
	for _, env := range []string{"", "A", "_", "aZ_09", "z", "BROWSER_TOKEN"} {
		if err := validTokenEnv(env); err != nil {
			t.Errorf("validTokenEnv(%q): %v", env, err)
		}
	}
	for _, env := range []string{"0TOKEN", "A-B", "A.B", "A=B", " A", "A ", "A\n", "A\r", "A\x00", "é", "Aé"} {
		if err := validTokenEnv(env); err == nil {
			t.Errorf("validTokenEnv(%q) accepted invalid name", env)
		}
		if token, err := browserToken(env); err == nil || token != "" {
			t.Errorf("browserToken(%q) did not reject invalid name", env)
		}
	}
}

func TestBrowserToken(t *testing.T) {
	if token, err := browserToken(""); err != nil || token != "" {
		t.Fatal("empty environment name should disable authentication")
	}
	const env = "OURO_BROWSER_AUTH_TEST_TOKEN"
	t.Setenv(env, "")
	if err := os.Unsetenv(env); err != nil {
		t.Fatal(err)
	}
	checkError := func() {
		t.Helper()
		token, err := browserToken(env)
		if token != "" || err == nil {
			t.Fatal("expected an error and no token")
		}
		if !strings.Contains(err.Error(), env) || strings.Contains(err.Error(), "secret") {
			t.Fatal("error must name the variable without revealing its value")
		}
	}
	checkError()
	for _, value := range []string{"", "secret\r", "secret\n", "secret\r\n"} {
		t.Setenv(env, value)
		checkError()
	}
	for _, value := range []string{"first-secret", "second-secret", " padded-secret "} {
		t.Setenv(env, value)
		token, err := browserToken(env)
		if err != nil || token != value {
			t.Fatal("expected the current environment value, unchanged")
		}
	}
}
