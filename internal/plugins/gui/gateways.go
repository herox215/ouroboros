package gui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ouroboros/plugin"
)

// The options of the window also hold the gateways of the profile, as the
// settings of the terminal UI do: the owner turns one on, signs in to its
// account and says who may talk to the agent through it. Which gateways there
// are, and what each asks, is up to the plugins that contribute a
// plugin.Gateway; they are read when they are shown, after all of them have
// started.
//
// The owner types a password here and nowhere else: it goes to the gateway
// once, lives no longer than the call, and is neither stored nor logged nor
// part of any snapshot. An error of a gateway is shown only where the gateway
// declares it safe to show.

// maxGatewayReason is how long the reason of a refusal may be, so that the
// notice stays one line.
const maxGatewayReason = 160

// errNoGateways is what a window answers that has no owner to change the
// gateways through, as tests open one.
var errNoGateways = errors.New("this window cannot change the gateways of the node")

// GatewayFieldInfo is one field of the form a gateway asks for, see
// plugin.GatewayField. The value of a secret is always empty.
type GatewayFieldInfo struct {
	Key   string   `json:"key"`
	Label string   `json:"label,omitempty"`
	Kind  string   `json:"kind"`
	Value string   `json:"value,omitempty"`
	List  []string `json:"list,omitempty"`
	On    bool     `json:"on,omitempty"`
	// Hint is a public text under the field; it never holds a value.
	Hint string `json:"hint,omitempty"`
}

// GatewayInfo is a gateway of the profile as the options show it: what the
// owner may see, never a password or a token.
type GatewayInfo struct {
	Name         string   `json:"name"`
	Title        string   `json:"title"`
	Enabled      bool     `json:"enabled"`
	Homeserver   string   `json:"homeserver,omitempty"`
	UserID       string   `json:"user_id,omitempty"`
	AllowedUsers []string `json:"allowed_users"`
	LoggedIn     bool     `json:"logged_in"`
	Status       string   `json:"status,omitempty"`
	// Fields is the form of a gateway that has one of its own; without them
	// the page shows the form of an account on a homeserver.
	Fields []GatewayFieldInfo `json:"fields,omitempty"`
}

// GatewayChangeBody is what the owner changes of a gateway, see
// plugin.GatewayChange: an empty Password keeps the login the gateway has.
type GatewayChangeBody struct {
	Enabled      bool                `json:"enabled"`
	Homeserver   string              `json:"homeserver,omitempty"`
	UserID       string              `json:"user_id,omitempty"`
	Password     string              `json:"password,omitempty"`
	AllowedUsers []string            `json:"allowed_users,omitempty"`
	Logout       bool                `json:"logout,omitempty"`
	Values       map[string]string   `json:"values,omitempty"`
	Lists        map[string][]string `json:"lists,omitempty"`
}

// Gateways lists the gateways of the profile with their settings.
func (w *Window) Gateways() ([]GatewayInfo, error) {
	return w.profile.gatewayList(context.Background())
}

// SaveGateway saves what the owner changed of the gateway name, and returns
// the gateways as they are then. A gateway that cannot use the change says why
// where it may; otherwise only that it did not take it.
func (w *Window) SaveGateway(name string, change GatewayChangeBody) ([]GatewayInfo, error) {
	return w.profile.saveGateway(context.Background(), name, change)
}

// gatewayList reads the settings of every gateway that has some, as
// Window.Gateways does.
func (p *profile) gatewayList(ctx context.Context) ([]GatewayInfo, error) {
	if p.gateways == nil {
		return nil, errNoGateways
	}
	out := []GatewayInfo{}
	for _, g := range p.gateways() {
		if g.Settings == nil {
			continue
		}
		s, err := g.Settings(ctx)
		if err != nil {
			// What a contribution returns may carry a credential.
			return nil, fmt.Errorf("the settings of the gateway %q cannot be read", g.Name)
		}
		out = append(out, gatewayInfo(g, s))
	}
	return out, nil
}

// gatewayInfo is what the owner is shown of the settings s of g. A secret
// field is emptied here, whatever the gateway put in it.
func gatewayInfo(g plugin.Gateway, s plugin.GatewaySettings) GatewayInfo {
	info := GatewayInfo{
		Name:         g.Name,
		Title:        g.Title,
		Enabled:      s.Enabled,
		Homeserver:   s.Homeserver,
		UserID:       s.UserID,
		AllowedUsers: append([]string{}, s.AllowedUsers...),
		LoggedIn:     s.LoggedIn,
		Status:       s.Status,
	}
	for _, f := range s.Fields {
		field := GatewayFieldInfo{Key: f.Key, Label: f.Label, Kind: f.Kind, Value: f.Value, List: append([]string(nil), f.List...), On: f.On, Hint: f.Hint}
		if f.Kind == "secret" {
			field.Value, field.List, field.On = "", nil, false
		}
		info.Fields = append(info.Fields, field)
	}
	return info
}

// saveGateway saves the change of the gateway name and lists the gateways
// anew, as Window.SaveGateway does.
func (p *profile) saveGateway(ctx context.Context, name string, body GatewayChangeBody) ([]GatewayInfo, error) {
	if p.gateways == nil {
		return nil, errNoGateways
	}
	change := plugin.GatewayChange{
		Enabled:      body.Enabled,
		Homeserver:   strings.TrimSpace(body.Homeserver),
		UserID:       strings.TrimSpace(body.UserID),
		Password:     body.Password,
		AllowedUsers: body.AllowedUsers,
		Logout:       body.Logout,
		Values:       body.Values,
		Lists:        body.Lists,
	}
	for _, g := range p.gateways() {
		if g.Name != name || g.Save == nil {
			continue
		}
		if err := g.Save(ctx, change); err != nil {
			return nil, gatewaySaveError{err}
		}
		return p.gatewayList(ctx)
	}
	return nil, fmt.Errorf("gateway %q is not available", name)
}

// gatewaySaveError is a failed save of a gateway. Only a reason the gateway
// declares safe to show is repeated; any other error of a contribution may
// carry a credential and stays behind the generic text.
type gatewaySaveError struct{ err error }

func (e gatewaySaveError) Error() string {
	const generic = "Gateway could not be saved; check the settings and try again"
	var refusal plugin.GatewayRefusal
	if !errors.As(e.err, &refusal) {
		return generic
	}
	// The notice is one line: no breaks, and short enough to be read.
	reason := strings.Join(strings.Fields(refusal.Reason), " ")
	if r := []rune(reason); len(r) > maxGatewayReason {
		reason = string(r[:maxGatewayReason]) + "…"
	}
	if reason == "" {
		return generic
	}
	return "Gateway not saved: " + reason
}
