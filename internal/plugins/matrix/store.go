// Package matrix contributes a profile-local Matrix messaging gateway.
package matrix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	transport "ouroboros/internal/matrix"
	"ouroboros/internal/safefile"
	"ouroboros/plugin"
)

type configuration struct {
	Enabled      bool     `json:"enabled"`
	Homeserver   string   `json:"homeserver"`
	UserID       string   `json:"user_id"`
	AllowedUsers []string `json:"allowed_users"`
	// HTTPAllowed is the homeserver the owner allowed plain HTTP for: it holds
	// only for exactly this URL and is empty for none.
	HTTPAllowed string `json:"http_allowed,omitempty"`
	Token       string `json:"token,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	Revision    string `json:"revision"`
}

type connectionStatus struct {
	Revision string
	Text     string
	At       time.Time
}

type store struct{ dir string }

func (s store) create() error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	return os.Chmod(s.dir, 0700)
}
func (s store) path(name string) string { return filepath.Join(s.dir, name) }
func (s store) load() (configuration, error) {
	var c configuration
	data, err := os.ReadFile(s.path("settings.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}
func (s store) save(c configuration) error {
	data, _ := json.MarshalIndent(c, "", "  ") // strings and booleans only: cannot fail
	return safefile.Write(s.path("settings.json"), append(data, '\n'), 0600)
}
func (s store) status(revision, text string) {
	data, _ := json.Marshal(connectionStatus{Revision: revision, Text: text, At: time.Now()})
	_ = safefile.Write(s.path("status.json"), data, 0600)
}
func (s store) snapshot() (plugin.GatewaySettings, error) {
	c, err := s.load()
	if err != nil {
		return plugin.GatewaySettings{}, err
	}
	state := plugin.GatewaySettings{Name: "matrix", Title: "Matrix", Enabled: c.Enabled, Homeserver: c.Homeserver, UserID: c.UserID, AllowedUsers: c.AllowedUsers, LoggedIn: c.Token != ""}
	state.Fields = []plugin.GatewayField{
		{Key: "homeserver", Label: "Homeserver", Kind: "text", Value: c.Homeserver, Hint: "HTTPS, or plain HTTP on this machine (127.0.0.1); HTTP elsewhere only when allowed below."},
		{Key: "allow_http", Label: "Allow plain HTTP for this homeserver (the access token then travels unencrypted)", Kind: "bool", On: c.HTTPAllowed != "" && c.HTTPAllowed == c.Homeserver,
			Hint: "The password at login and the access token afterwards are then sent unencrypted. Use it only in a network you trust, such as a tailnet."},
		{Key: "user_id", Label: "User ID", Kind: "text", Value: c.UserID},
		{Key: "password", Label: "Password", Kind: "secret"},
		{Key: "allowed_users", Label: "Allowed user IDs", Kind: "list", List: append([]string(nil), c.AllowedUsers...)},
	}
	switch {
	case !c.Enabled:
		state.Status = "Disabled"
	case c.Token == "":
		state.Status = "Not logged in"
	default:
		state.Status = "Waiting for the node"
		var st connectionStatus
		if data, err := os.ReadFile(s.path("status.json")); err == nil && json.Unmarshal(data, &st) == nil && st.Revision == c.Revision && time.Since(st.At) < 15*time.Second {
			state.Status = st.Text
		}
	}
	return state, nil
}

func userID(s string) bool {
	// Compare complete Matrix IDs, never display names or localparts.
	i := strings.IndexByte(s, ':')
	return strings.HasPrefix(s, "@") && i > 1 && i < len(s)-1 && !strings.ContainsAny(s, " \t\r\n")
}

// homeserverRefusal is the refusal of a homeserver URL. It is a
// plugin.GatewayRefusal for the owner and carries the rejected value, which
// the log shows redacted, to tell this check from every other refusal.
type homeserverRefusal struct {
	plugin.GatewayRefusal
	homeserver string
}

func (e homeserverRefusal) Unwrap() error { return e.GatewayRefusal }
func (e homeserverRefusal) Is(target error) bool {
	return target == transport.ErrServerRefused
}

// logRefusal writes a refused homeserver to the log, without credentials,
// query or fragment of the URL; any other error is not its business.
func logRefusal(log func(string, ...any), err error) {
	var refusal homeserverRefusal
	if errors.As(err, &refusal) {
		log("matrix gateway: homeserver %q refused: %s", transport.RedactServer(refusal.homeserver), refusal.Reason)
	}
}

// httpAllowance is the homeserver of a normalized change that the owner
// allowed plain HTTP for: the one it names, or none.
func httpAllowance(change plugin.GatewayChange) string {
	if change.Values["allow_http"] == "true" {
		return change.Homeserver
	}
	return ""
}

func normalize(change plugin.GatewayChange) (plugin.GatewayChange, error) {
	for key, value := range change.Values {
		switch key {
		case "allow_http":
			if value != "true" && value != "false" {
				return change, plugin.GatewayRefusal{Reason: "unknown value for allowing plain HTTP; use true or false"}
			}
		case "homeserver":
			change.Homeserver = value
		case "user_id":
			change.UserID = value
		case "password":
			change.Password = value
		default:
			return change, plugin.GatewayRefusal{Reason: "unknown Matrix setting"}
		}
	}
	for key, list := range change.Lists {
		if key != "allowed_users" {
			return change, plugin.GatewayRefusal{Reason: "unknown Matrix list setting"}
		}
		change.AllowedUsers = list
	}
	change.Homeserver = strings.TrimRight(strings.TrimSpace(change.Homeserver), "/")
	change.UserID = strings.TrimSpace(change.UserID)
	if change.Homeserver != "" {
		if err := transport.ValidateServer(change.Homeserver, httpAllowance(change)); err != nil {
			return change, homeserverRefusal{plugin.GatewayRefusal{Reason: err.Error()}, change.Homeserver}
		}
	}
	if change.UserID != "" && !userID(change.UserID) {
		return change, plugin.GatewayRefusal{Reason: "use a complete Matrix user ID, such as @luna:example.org"}
	}
	seen := map[string]bool{}
	var allowed []string
	for _, u := range change.AllowedUsers {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !userID(u) {
			return change, plugin.GatewayRefusal{Reason: fmt.Sprintf("invalid Matrix user ID %q; use @user:server", u)}
		}
		if u == change.UserID {
			return change, plugin.GatewayRefusal{Reason: "the gateway account cannot be an allowed user"}
		}
		if !seen[u] {
			allowed = append(allowed, u)
			seen[u] = true
		}
	}
	sort.Strings(allowed)
	change.AllowedUsers = allowed
	if (change.Enabled || change.Password != "") && (change.Homeserver == "" || change.UserID == "") {
		return change, plugin.GatewayRefusal{Reason: "homeserver and Matrix user ID are required"}
	}
	return change, nil
}
