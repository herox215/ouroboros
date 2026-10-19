package contacts

import (
	"slices"
	"time"
)

// DecideRequest applies a question's decision only if the persisted request
// still exactly matches what the owner saw. A refreshed or replaced request
// requires its own approval, including a proposal that became incoming.
func (s *Store) DecideRequest(expected Request, approve bool) (Request, bool, error) {
	var ok bool
	err := s.update(func(d *Data) error {
		i := slices.IndexFunc(d.Incoming, match(expected.PublicKey))
		if i < 0 || d.Incoming[i] != expected {
			return errUnchanged
		}
		ok = true
		d.Incoming = slices.Delete(d.Incoming, i, i+1)
		if !approve {
			if !expected.Proposed && !slices.Contains(d.Blocked, expected.PublicKey) {
				d.Blocked = append(d.Blocked, expected.PublicKey)
			}
			return nil
		}
		d.Outgoing = slices.DeleteFunc(d.Outgoing, match(expected.PublicKey))
		if expected.Proposed {
			expected.At = time.Now()
			d.Outgoing = append(d.Outgoing, Request{PublicKey: expected.PublicKey, Name: expected.Name, Model: expected.Model, Addr: expected.Addr, At: expected.At})
		} else if !slices.ContainsFunc(d.Trusted, trusted(expected.PublicKey)) {
			d.Trusted = append(d.Trusted, Contact{PublicKey: expected.PublicKey, Name: expected.Name, Model: expected.Model, Added: time.Now()})
		}
		return nil
	})
	if err != nil {
		return Request{}, false, err
	}
	return expected, ok, nil
}
