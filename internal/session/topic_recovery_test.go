package session

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
)

// Exercise actual thread input while Send is blocked in CheckTopic, then let
// the router consume that input just as the agent does between tool calls.
func TestTopicSendReverifiesInFlightRequest(t *testing.T) {
	for _, corrective := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrective=%t", corrective), func(t *testing.T) {
			s, a, target, first := topicFixture(t)
			const request = "Add the login regression test"
			input := "What is the weather tomorrow?"
			if corrective {
				input = "No, don't add the login test; I meant the billing issue"
			}
			s.mu.Lock()
			weather := s.get("weather-concern")
			s.emit(weather, Event{Kind: Message, Text: "Weather forecast"})
			second := make(chan queued, 2)
			weather.asked = second
			thread := s.get(ThreadID)
			s.mu.Unlock()

			entered, release := make(chan struct{}), make(chan struct{})
			checks := 0
			a.checkTopic = func(ctx context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
				if check.Message == request {
					checks++
					if checks == 1 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return assistant.TopicDecision{}, ctx.Err()
						}
					} else {
						if !topicHas(check.Context, "user", input) {
							t.Error("reverification did not see newer thread input")
						}
						if corrective {
							return assistant.TopicDecision{Disposition: assistant.TopicNew, Reason: "owner corrected the requested concern"}, nil
						}
					}
				}
				return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "independently verified"}, nil
			}
			done := make(chan error, 1)
			a.run = func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
				if text != request {
					return "acknowledged", nil
				}
				_, _, err := (threading{s}).Send(ctx, target.id, "")
				if corrective {
					if err == nil || !strings.Contains(err.Error(), "new concern") {
						done <- fmt.Errorf("corrective context not rejected: %v", err)
						return "", err
					}
					err = nil // the semantic refusal is the expected outcome
				} else if err != nil {
					done <- err
					return "", err
				}
				messages := s.steered(thread)
				if len(messages) != 1 || messages[0].Content != input {
					done <- fmt.Errorf("new input not retained: %+v", messages)
					return "", nil
				}
				if !corrective {
					_, _, err = (threading{s}).Send(ctx, weather.id, messages[0].Content)
				}
				done <- err
				return "acknowledged", nil // no agent.End dependency
			}
			if err := s.Ask(context.Background(), ThreadID, request); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("CheckTopic did not start")
			}
			if err := s.Ask(context.Background(), ThreadID, input); err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handoff did not complete")
			}
			if checks != 2 {
				t.Fatalf("got %d checks for first request, want 2", checks)
			}
			for _, delivery := range []struct {
				ch   chan queued
				want string
			}{{first, request}, {second, input}} {
				select {
				case got := <-delivery.ch:
					if corrective || got.text != delivery.want {
						t.Fatalf("unexpected delivery: %+v", got)
					}
				default:
					if !corrective {
						t.Fatal("request lost instead of dispatched")
					}
				}
				select {
				case got := <-delivery.ch:
					t.Fatalf("duplicate delivery: %+v", got)
				default:
				}
			}
		})
	}
}
