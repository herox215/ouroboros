package peers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
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

// A rejection of a request that changed since it was shown is refused like an
// approval of one.
func TestConnectionQuestionCannotRejectChangedRequest(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
	data, _ := alice.contacts.Snapshot()
	err := alice.network().approval(t.Context(), data.Incoming[0], func(context.Context, json.RawMessage) (string, error) {
		alice.contacts.AddIncoming(bob.PublicKey, "changed", "", "")
		return "Reject connection", nil
	})
	if err != ErrNoRequest {
		t.Fatalf("got %v", err)
	}
}

// A decision that cannot be stored is reported, for either answer, and the
// request stays.
func TestConnectionQuestionReportsUnstoredDecisions(t *testing.T) {
	for _, answer := range []string{"Approve connection", "Reject connection"} {
		t.Run(answer, func(t *testing.T) {
			alice, bob := serve(t, "alice"), serve(t, "bob")
			alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
			data, _ := alice.contacts.Snapshot()
			repair := breakContacts(t, alice)
			defer repair()
			err := alice.network().approval(t.Context(), data.Incoming[0], func(context.Context, json.RawMessage) (string, error) {
				return answer, nil
			})
			if err == nil {
				t.Fatal("unstored decision reported as made")
			}
		})
	}
}

// A decision that waits when the owner cancels ends with the conversation.
func TestApprovalsStopWhenTheOwnerCancels(t *testing.T) {
	alice, bob, carol := serve(t, "alice"), serve(t, "bob"), serve(t, "carol")
	alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
	alice.contacts.AddIncoming(carol.PublicKey, "carol", "", "")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		alice.network().Approvals(ctx, func(context.Context, json.RawMessage) (string, error) {
			cancel()
			return "", context.Canceled
		}, nil)
		close(done)
	}()
	<-done
	if data, _ := alice.contacts.Snapshot(); len(data.Incoming) != 2 {
		t.Fatalf("a canceled approval changed the requests: %+v", data.Incoming)
	}
}

// A contact list that cannot be read is reported, and the wait goes on until
// the context ends.
func TestApprovalsReportUnreadableContacts(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
	repair := breakContacts(t, alice)
	defer repair()
	ctx, cancel := context.WithCancel(t.Context())
	logged := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		alice.network().Approvals(ctx, nil, func(format string, args ...any) {
			select {
			case logged <- format:
			default:
			}
		})
		close(done)
	}()
	select {
	case <-logged:
	case <-done:
		t.Fatal("approvals ended without a report")
	}
	cancel()
	<-done
}

// An approval that fails while the owner is there is reported, and the wait
// goes on.
func TestApprovalsReportAFailedApproval(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
	ctx, cancel := context.WithCancel(t.Context())
	logged := make(chan string, 1)
	done := make(chan struct{})
	go func() {
		alice.network().Approvals(ctx, func(context.Context, json.RawMessage) (string, error) {
			return "yes", nil
		}, func(format string, args ...any) {
			select {
			case logged <- format:
				cancel()
			default:
			}
		})
		close(done)
	}()
	<-done
	if format := <-logged; format != "peer connection approval: %v" {
		t.Fatalf("reported %q", format)
	}
}

// An approval the owner grants is no failure: nothing is reported about it.
func TestApprovalsDoNotReportAGrantedApproval(t *testing.T) {
	alice, bob := serve(t, "alice"), serve(t, "bob")
	alice.contacts.AddIncoming(bob.PublicKey, "bob", "", "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logged := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		alice.network().Approvals(ctx, func(context.Context, json.RawMessage) (string, error) {
			return "Approve connection", nil
		}, func(format string, args ...any) {
			select {
			case logged <- format:
			default:
			}
		})
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if trusted, _, _ := alice.contacts.State(bob.PublicKey); trusted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval not stored")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	select {
	case format := <-logged:
		t.Fatalf("reported %q about a granted approval", format)
	default:
	}
}
