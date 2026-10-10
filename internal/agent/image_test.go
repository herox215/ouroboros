package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"ouroboros/internal/openai"
)

// image is a picture told apart by its first byte.
func image(b byte) openai.Image { return openai.Image{Type: "image/png", Data: []byte{b}} }

// A tool shows the LLM pictures along with its result, and the conversation
// keeps them. A tool that fails shows none.
func TestToolShowsImages(t *testing.T) {
	llm := &fakeLLM{}
	look := Tool{Name: "look", Schema: `{"type":"object"}`, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		if !Show(ctx, image(1), image(2)) {
			t.Error("a tool should be able to show pictures")
		}
		return "two pictures", nil
	}}
	broken := Tool{Name: "broken", Schema: `{"type":"object"}`, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		Show(ctx, image(3))
		return "", errors.New("broke")
	}}
	a := New(llm, "gpt", "sys", look, broken)

	if _, err := a.Respond(context.Background(), "c1", "call look"); err != nil {
		t.Fatal(err)
	}
	result := llm.msgs[3]
	if result.Role != "tool" || result.Content != "two pictures" || !slices.EqualFunc(result.Images, []openai.Image{image(1), image(2)}, sameImage) {
		t.Fatalf("result %+v", result)
	}
	a.Respond(context.Background(), "c1", "and now?")
	if got := llm.msgs[3].Images; len(got) != 2 {
		t.Fatalf("the next answer should still see the pictures: %+v", llm.msgs)
	}

	a.Respond(context.Background(), "c2", "call broken")
	if result := llm.msgs[3]; result.Content != "error: broke" || result.Images != nil {
		t.Fatalf("a failed call should show no pictures: %+v", result)
	}
}

func sameImage(a, b openai.Image) bool { return a.Type == b.Type && string(a.Data) == string(b.Data) }

// Outside of a call of a tool, pictures go nowhere.
func TestShowOutsideOfACall(t *testing.T) {
	if Show(context.Background(), image(1)) {
		t.Fatal("pictures should not be shown outside of a call")
	}
}

// An answer that failed is remembered without its pictures: a model that
// takes none would fail with them again.
func TestAbandonForgetsTheImages(t *testing.T) {
	msgs := []openai.Message{
		{Role: "user", Content: "look"},
		{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "c1"}}},
		{Role: "tool", ToolCallID: "c1", Content: "a picture", Images: []openai.Image{image(1)}},
	}
	for _, m := range Abandon(msgs) {
		if m.Images != nil {
			t.Fatalf("an abandoned answer should keep no pictures: %+v", m)
		}
	}
	if msgs[2].Images == nil {
		t.Fatal("Abandon should not change what it is handed")
	}
}

// A conversation keeps the last maxImages pictures, and only those.
func TestLastImages(t *testing.T) {
	images := func(from, n int) []openai.Image {
		var out []openai.Image
		for i := range n {
			out = append(out, image(byte(from+i)))
		}
		return out
	}
	msgs := []openai.Message{{Images: images(0, 2)}, {Images: images(2, 3)}, {}, {Images: images(5, 3)}, {Images: images(8, 4)}}
	got := lastImages(msgs)
	want := [][]openai.Image{nil, images(4, 1), nil, images(5, 3), images(8, 4)}
	for i := range want {
		if !slices.EqualFunc(got[i].Images, want[i], sameImage) {
			t.Fatalf("message %d keeps %v, want %v", i, got[i].Images, want[i])
		}
	}
	if len(msgs[0].Images) != 2 || len(msgs[1].Images) != 3 {
		t.Fatal("lastImages should not change what it is handed")
	}
	exactly := []openai.Message{{Images: images(0, maxImages-3)}, {Images: images(5, 3)}}
	if got := lastImages(exactly); &got[0] != &exactly[0] {
		t.Fatal("a conversation with maxImages pictures should stay as it is")
	}
	// The first message is looked at as well.
	first := lastImages([]openai.Message{{Images: images(0, maxImages+1)}})
	if !slices.EqualFunc(first[0].Images, images(1, maxImages), sameImage) {
		t.Fatalf("the first message keeps %v", first[0].Images)
	}
}

// The pictures do not count for the size of a conversation: a single one
// would cut all that came before it.
func TestImagesDoNotCountForTheSize(t *testing.T) {
	plain := []openai.Message{{Role: "tool", Content: "a picture"}}
	shown := []openai.Message{{Role: "tool", Content: "a picture", Images: []openai.Image{{Type: "image/png", Data: make([]byte, 1<<20)}}}}
	if a, b := sizes(plain), sizes(shown); a[0] != b[0] {
		t.Fatalf("sizes %d and %d should be the same", a[0], b[0])
	}
}

// A copy of messages has pictures of its own to change.
func TestCopyMessagesCopiesTheImages(t *testing.T) {
	msgs := []openai.Message{{Images: []openai.Image{image(1)}}}
	copyMessages(msgs)[0].Images[0] = image(2)
	if msgs[0].Images[0].Data[0] != 1 {
		t.Fatal("changing the copy changed the original")
	}
}

// A picture the conversation shows already is not sent again: the result
// names the call it came with. Another picture is.
func TestPicturesAreShownOnce(t *testing.T) {
	llm := &fakeLLM{}
	shown := []openai.Image{image(1)}
	look := Tool{Name: "look", Schema: `{"type":"object"}`, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		Show(ctx, shown...)
		return "pictures", nil
	}}
	a := New(llm, "gpt", "sys", look)
	a.Respond(context.Background(), "c1", "call look")
	shown = []openai.Image{image(1), image(2)}
	a.Respond(context.Background(), "c1", "call look")
	result := llm.msgs[len(llm.msgs)-1]
	if !slices.EqualFunc(result.Images, []openai.Image{image(2)}, sameImage) ||
		result.Content != "pictures\n[The picture is the same as the one shown above with the result of the call c1: it is not sent again.]" {
		t.Fatalf("result %+v", result)
	}
	if first := llm.msgs[3]; len(first.Images) != 1 {
		t.Fatalf("the first result should keep its picture: %+v", first)
	}
}

// A tool gives the owner a remark on its call, the last it gave, with the
// step that ends it; a tool it runs gives its own. Outside of a call a
// remark goes nowhere.
func TestRemark(t *testing.T) {
	inner := Tool{Name: "inner", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		Remark(ctx, "of inner")
		return "", nil
	}}
	outer := Tool{Name: "outer", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		Remark(ctx, "first")
		Invoke(ctx, inner, nil)
		Remark(ctx, "of outer")
		return "", nil
	}}
	var steps []Step
	ctx := context.WithValue(context.Background(), observerKey{}, Observer{Step: func(s Step) { steps = append(steps, s) }})
	Invoke(ctx, outer, nil)
	if len(steps) != 4 || steps[2].Tool != "inner" || steps[2].Remark != "of inner" || steps[3].Remark != "of outer" || steps[0].Remark != "" {
		t.Fatalf("steps %+v", steps)
	}
	if Remark(context.Background(), "nowhere") {
		t.Fatal("a remark outside of a call should go nowhere")
	}
}
