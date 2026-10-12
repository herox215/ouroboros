package session

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"ouroboros/internal/history"
)

// ServiceInfo is a service a tool keeps running past the answers, like a
// server, see API.Services.
type ServiceInfo struct {
	// ID names it for StopService; Name is what the agent knows it by.
	ID   string `json:"id"`
	Name string `json:"name"`
	// PeerKey is the public key of the peer it runs for, "" for one of the
	// owner; Peer is its name, as Services tells it.
	PeerKey string    `json:"peer_key,omitempty"`
	Peer    string    `json:"peer,omitempty"`
	Command string    `json:"command"`         // its first line
	Port    int       `json:"port,omitempty"`  // it listens on, if it was told
	Since   time.Time `json:"since"`           // it started
	Until   time.Time `json:"until,omitempty"` // it is stopped, once its time is up
}

// Services are the services a tool keeps running, for API.Services: List
// are those that run, and Stop stops the one with the ID id, with
// everything it started, and reports whether it ran.
type Services struct {
	List func() []ServiceInfo
	Stop func(id string) bool
}

// ErrNoService means a service that does not run.
var ErrNoService = errors.New("no such service runs")

// ServicesFrom tells the Service where the services of the node are: all is
// asked every time, so that what a plugin contributes later is in it.
func (s *Service) ServicesFrom(all func() []Services) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.services = all
}

// sources are what ServicesFrom was told, none without it.
func (s *Service) sources() []Services {
	s.mu.Lock()
	all := s.services
	s.mu.Unlock()
	if all == nil {
		return nil
	}
	return all() // the tools are asked without s.mu
}

// Services are the services that run past the answers, the oldest first,
// those of peers with their names.
func (s *Service) Services(context.Context) ([]ServiceInfo, error) {
	var list []ServiceInfo
	for _, source := range s.sources() {
		list = append(list, source.List()...)
	}
	s.mu.Lock()
	for i, sv := range list {
		if sv.PeerKey != "" {
			party := history.Party(sv.PeerKey)
			list[i].Peer = party
			if s.archive != nil {
				list[i].Peer = cmp.Or(s.archive.peer(party), party)
			}
		}
	}
	s.mu.Unlock()
	slices.SortFunc(list, func(a, b ServiceInfo) int { return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.ID, b.ID)) })
	return list, nil
}

// StopService stops the service id, with everything it started. It fails
// with ErrNoService if that service does not run.
func (s *Service) StopService(_ context.Context, id string) error {
	for _, source := range s.sources() {
		if source.Stop(id) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrNoService, id)
}
