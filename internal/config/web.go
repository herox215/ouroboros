package config

import "fmt"

// DefaultWebPort is where the page of the thread is served where the
// configuration does not say: the node's own port is 41337 and the one of
// discovery 41338.
const DefaultWebPort = 41340

// MinWebPort and MaxWebPort are the ports web.port can be: below 1024 is for
// root, and above is no port.
const (
	MinWebPort = 1024
	MaxWebPort = 65535
)

// Web is the page of the thread on the network: the GUI of ouro in a browser.
// It asks for the user and the password before it shows anything, and it is
// served only while the node serves.
type Web struct {
	// Enabled opens the page to the network once the node serves.
	Enabled bool `json:"enabled,omitempty"`
	// Port is where the page is served, DefaultWebPort where it says none.
	Port int `json:"port,omitempty"`
	// User and Password are what the page asks for at its login. Password is
	// kept as an argon2id hash, never as the password itself, and never
	// leaves the node: see package web.
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

// PortOr is the port the page is served on: web.port, or DefaultWebPort.
func (w Web) PortOr() int {
	if ValidWebPort(w.Port) != nil {
		return DefaultWebPort
	}
	return w.Port
}

// Ready reports whether the page can be served at all: it is enabled and it
// has a user and a password. One that is not enabled is served to nobody.
func (w Web) Ready() bool { return w.Enabled && w.User != "" && w.Password != "" }

// ValidWebPort reports a port that cannot be served on: below MinWebPort or
// above MaxWebPort.
func ValidWebPort(port int) error {
	if port < MinWebPort || port > MaxWebPort {
		return fmt.Errorf("web.port is a number from %d to %d", MinWebPort, MaxWebPort)
	}
	return nil
}

// ValidWebUser reports a user name that cannot be asked for: one that is
// empty, or one with control characters, which no login form sends.
func ValidWebUser(user string) error {
	if user == "" {
		return fmt.Errorf("web.user has to be a name")
	}
	if len(user) > 64 {
		return fmt.Errorf("web.user is at most 64 bytes")
	}
	for _, r := range user {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("web.user has a control character")
		}
	}
	return nil
}
