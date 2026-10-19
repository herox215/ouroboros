package peers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestConnectionQuestionFailsClosed(t *testing.T) {
	for _, mode := range []string{"missing", "error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			alice, bob := serve(t, "alice"), serve(t, "bob")
			alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
			data, _ := alice.contacts.Snapshot()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var ask func(context.Context, json.RawMessage) (string, error)
			if mode != "missing" {
				ask = func(context.Context, json.RawMessage) (string, error) {
					if mode == "cancelled" {
						cancel()
						return "Approve connection", nil
					}
					return "Approve connection", errors.New("owner gone")
				}
			}
			if err := alice.network().approval(ctx, data.Incoming[0], ask); err == nil {
				t.Fatal("expected failure")
			}
			data, _ = alice.contacts.Snapshot()
			if len(data.Trusted) != 0 || len(data.Incoming) != 1 || bob.hits != 0 {
				t.Fatalf("not fail closed: %+v", data)
			}
		})
	}
}

func TestConnectionQuestionRequiresExplicitApproval(t *testing.T) {
	for _, proposed := range []bool{false, true} {
		for _, answer := range []string{"Approve connection", "Reject connection", "yes", "", "Approve connection\n"} {
			t.Run(answer+map[bool]string{true: " proposal", false: " incoming"}[proposed], func(t *testing.T) {
				alice, bob := serve(t, "alice"), serve(t, "bob")
				network := alice.network()
				if proposed {
					alice.contacts.Propose(bob.PublicKey, "bob", "", bob.Addr())
				} else {
					alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
				}
				data, _ := alice.contacts.Snapshot()
				err := network.approval(t.Context(), data.Incoming[0], func(ctx context.Context, q json.RawMessage) (string, error) {
					var question struct {
						Question string
						Options  []string
					}
					if json.Unmarshal(q, &question) != nil || len(question.Options) != 2 {
						t.Fatalf("invalid question: %s", q)
					}
					trusted, _, _ := alice.contacts.State(bob.PublicKey)
					if trusted || bob.hits != 0 {
						t.Fatal("connected before approval")
					}
					return answer, nil
				})
				data, _ = alice.contacts.Snapshot()
				approved := answer == "Approve connection"
				rejected := answer == "Reject connection"
				if approved || rejected {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil {
					t.Fatal("ambiguous answer accepted")
				}
				trusted, _, _ := alice.contacts.State(bob.PublicKey)
				if trusted != (approved && !proposed) {
					t.Fatalf("trust=%v", trusted)
				}
				if (len(data.Incoming) == 0) != (approved || rejected) {
					t.Fatalf("incoming: %+v", data.Incoming)
				}
				if proposed && approved && len(data.Outgoing) != 1 {
					t.Fatal("proposal not sent")
				}
				if (!proposed || !approved) && bob.hits != 0 {
					t.Fatal("unexpected network contact")
				}
			})
		}
	}
}

func TestConnectionQuestionCannotApproveChangedRequest(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	alice.contacts.Propose(bob.PublicKey, "bob", "", bob.Addr())
	data, _ := alice.contacts.Snapshot()
	err := alice.network().approval(t.Context(), data.Incoming[0], func(context.Context, json.RawMessage) (string, error) {
		alice.contacts.AddIncoming(bob.PublicKey, "changed", "", bob.Addr())
		return "Approve connection", nil
	})
	if err != ErrNoRequest {
		t.Fatalf("got %v", err)
	}
	data, _ = alice.contacts.Snapshot()
	if len(data.Trusted) != 0 || len(data.Outgoing) != 0 || len(data.Incoming) != 1 || data.Incoming[0].Proposed {
		t.Fatalf("%+v", data)
	}
}
