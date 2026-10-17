package session

import (
	"context"
	"errors"
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
