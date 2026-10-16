package plugin

import "context"

// Gateway is a profile-local external messaging access contributed by a plugin.
// Frontends read it when opening settings, after all plugins have started.
type Gateway struct {
	Name     string
	Title    string
	Settings func(context.Context) (GatewaySettings, error)
	Save     func(context.Context, GatewayChange) error
}

// GatewaySettings contains public settings only, never tokens or passwords.
type GatewaySettings struct {
	Name         string
	Title        string
	Enabled      bool
	Homeserver   string
	UserID       string
	AllowedUsers []string
	LoggedIn     bool
	Status       string
	// Fields optionally defines a gateway-specific form. A frontend must not
	// assume every protocol uses a homeserver, password or Matrix user ID.
	Fields []GatewayField
}

// GatewayField describes one public form field. Secret values are always empty
// in snapshots. Kind is text, secret, list or bool.
type GatewayField struct {
	Key, Label, Kind, Value string
	List                    []string
	On                      bool
}

// GatewayChange is supplied only by a local owner frontend. An empty password
// keeps the current login; passwords must never be persisted or returned.
type GatewayChange struct {
	Enabled      bool
	Homeserver   string
	UserID       string
	Password     string
	AllowedUsers []string
	Logout       bool
	// Values and Lists carry schema-defined fields, including write-only secrets.
	// Gateways must validate keys and must never return submitted secrets.
	Values map[string]string
	Lists  map[string][]string
}
