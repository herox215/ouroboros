package chatgpt

import (
	"context"
	"ouroboros/internal/chatgpt/chatgpttest"
	"ouroboros/internal/llm"
	"ouroboros/internal/openai"
	"testing"
)

func TestOutputUsage(t *testing.T) {
	srv, o := fake(t)
	c := signIn(t, srv, o)
	srv.Set(func(s *chatgpttest.State) {
		s.Responses = []chatgpttest.Response{{Events: []string{
			`{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":42,"input_tokens_details":{"cached_tokens":80}}}}`,
		}}}
	})
	got, err := Provider(o).Model(llm.Unsaved(&c)).Complete(context.Background(), "m", []openai.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || got.Usage.Output != 42 || !got.Usage.InPlan || got.Usage.Cached != 80 {
		t.Fatalf("%+v %v", got, err)
	}
}
