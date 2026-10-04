package openai_test

import (
	"context"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/plugins/openai"
	"ouroboros/plugin"
)

// The plugin provides the LLM: the model at the endpoint of the node.
func TestProvidesTheLLM(t *testing.T) {
	app, err := cli.NewWith(openai.Plugin())
	if err != nil {
		t.Fatal(err)
	}
	llm, ok := cli.Service[plugin.LLM](app)
	if !ok {
		t.Fatal("no LLM")
	}
	model := llm(plugin.Endpoint{BaseURL: openaitest.NewServer(t), APIKey: openaitest.Key})
	reply, err := model.Complete(context.Background(), "gpt-4o", []plugin.ChatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil || reply.Content != "[gpt-4o] turns=0 last=hi" {
		t.Fatalf("got %+v, %v", reply, err)
	}
	// Another key is another client: the wrong one is refused.
	if _, err := llm(plugin.Endpoint{BaseURL: openaitest.NewServer(t), APIKey: "sk-bad"}).Complete(context.Background(), "gpt-4o", nil, nil); err == nil {
		t.Fatal("want error")
	}
}
