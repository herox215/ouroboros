package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
)

type imageModel struct{ seen chan []openai.Message }

func (m imageModel) Complete(_ context.Context, _ string, messages []openai.Message, _ []openai.Tool) (openai.Message, error) {
	m.seen <- messages
	return openai.Message{Content: "seen"}, nil
}

func TestImageInputReachesModel(t *testing.T) {
	for _, socket := range []bool{false, true} {
		name := "local"
		if socket {
			name = "socket"
		}
		t.Run(name, func(t *testing.T) {
			seen := make(chan []openai.Message, 1)
			model := agent.New(imageModel{seen}, "test", "system")
			gate := make(chan struct{})
			a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
				select {
				case <-gate:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return model.Run(ctx, "s1", text, agent.Observer{})
			}}
			s := service(t, a)
			var input ImageAPI = s
			var api API = s
			if socket {
				c := NewClient(serve(t, Handler(s)).dir)
				input, api = c, c
			}
			ctx := WithMessageSource(t.Context(), MessageSource{Interface: "api"})
			_, live, err := api.Watch(ctx, "s1")
			if err != nil {
				t.Fatal(err)
			}
			images := []openai.Image{{Type: "image/png", Data: []byte("picture")}}
			caption := ""
			if socket {
				caption = "look here"
			}
			if err := input.AskImages(ctx, "s1", caption, images); err != nil {
				t.Fatal(err)
			}
			images[0].Data[0] = 'X'
			images[0].Type = "changed"
			if err := input.AskImages(ctx, "s1", "another", []openai.Image{{Type: "image/png", Data: []byte("p")}}); !errors.Is(err, ErrBusy) {
				t.Fatalf("busy: %v", err)
			}
			close(gate)
			events := until(t, live, Answer)
			if events[0].Text != caption || events[0].Source.Interface != "api" {
				t.Fatalf("input event: %+v", events[0])
			}
			if events[len(events)-1].Text != "seen" {
				t.Fatalf("answer: %+v", events)
			}
			messages := <-seen
			last := messages[len(messages)-1]
			if last.Role != "user" || !strings.Contains(last.Content, "source interface = api") || !strings.HasSuffix(last.Content, caption) || len(last.Images) != 1 || last.Images[0].Type != "image/png" || string(last.Images[0].Data) != "picture" {
				t.Fatalf("model input: %+v", last)
			}
			for _, images := range [][]openai.Image{nil, {{Type: "image/png"}}, {{Type: "text/plain", Data: []byte("bad")}}} {
				want := ErrImage
				if len(images) == 0 {
					want = ErrEmpty
				}
				if err := input.AskImages(ctx, "s2", "", images); !errors.Is(err, want) {
					t.Fatalf("invalid input: %v, want %v", err, want)
				}
			}
		})
	}
}

func TestPendingQuestionImageReplies(t *testing.T) {
	for _, socket := range []bool{false, true} {
		for _, caption := range []string{"", "  new topic\nlook here  "} {
			t.Run(fmt.Sprintf("socket=%t/caption=%q", socket, caption), func(t *testing.T) {
				gate := make(chan struct{})
				a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
					reply, err := agent.AskOwnerReply(ctx, json.RawMessage(`{"question":"Which?","options":["a","b"]}`))
					<-gate
					if reply.Text != caption || reply.Source.Interface != "api" || len(reply.Images) != 1 || string(reply.Images[0].Data) != "picture" {
						t.Errorf("reply: %+v", reply)
					}
					return "received", err
				}}
				s := service(t, a)
				var api API = s
				var input ReplyImageAPI = s
				if socket {
					c := NewClient(serve(t, Handler(s)).dir)
					api, input = c, c
				}
				ctx := WithMessageSource(t.Context(), MessageSource{Interface: "api"})
				_, live, err := api.Watch(ctx, "s1")
				if err != nil {
					t.Fatal(err)
				}
				if err := api.Ask(ctx, "s1", "go"); err != nil {
					t.Fatal(err)
				}
				until(t, live, Asked)
				if err := input.SteerImages(ctx, "s1", "", []openai.Image{{Type: "text/plain", Data: []byte("bad")}}); !errors.Is(err, ErrImage) {
					t.Fatalf("validation: %v", err)
				}
				images := []openai.Image{{Type: "image/png", Data: []byte("picture")}}
				if err := input.SteerImages(ctx, "s1", caption, images); err != nil {
					t.Fatal(err)
				}
				images[0].Data[0] = 'X'
				close(gate)
				events := until(t, live, Answer)
				if !events[0].Done || events[0].Text != caption || events[len(events)-1].Text != "received" {
					t.Fatalf("events: %+v", events)
				}
			})
		}
	}
}

func TestImageReplyRelayedAndQueued(t *testing.T) {
	for _, relay := range []bool{false, true} {
		t.Run(fmt.Sprint(relay), func(t *testing.T) {
			s := service(t, &fakeAgent{})
			ctx := WithMessageSource(t.Context(), MessageSource{Interface: "api"})
			s.mu.Lock()
			target := s.get("s1")
			s.emit(target, Event{Kind: Message, Text: "Discuss the supplied picture"})
			target.cancel = func() {}
			var pending chan queued
			id := "s1"
			if relay {
				pending = make(chan queued, 1)
				target.asked = pending
				s.get(ThreadID).questions = []*relayed{{se: target}}
				id = ThreadID
			}
			s.mu.Unlock()
			images := []openai.Image{{Type: "image/png", Data: []byte("picture")}}
			if err := s.SteerImages(ctx, id, "", images); err != nil {
				t.Fatal(err)
			}
			images[0].Data[0] = 'X'
			s.mu.Lock()
			var got queued
			if relay {
				select {
				case got = <-pending:
				default:
					s.mu.Unlock()
					t.Fatal("checked image reply was not delivered")
				}
			} else {
				got = target.queue[0]
			}
			target.cancel = nil
			target.asked = nil
			s.mu.Unlock()
			if len(got.images) != 1 || string(got.images[0].Data) != "picture" || got.source.Interface != "api" {
				t.Fatalf("delivery: %+v", got)
			}
		})
	}
}
