package assistant

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/workdir"
)

func TestProjectDirectoryPromptIsPerAnswer(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return nil }, Workspace: "/work/me"}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	for _, directory := range []string{"/projects/one", "/projects/two", ""} {
		got, err := a.Ask(workdir.With(context.Background(), directory), "chat", "?system", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "/work/me") {
			t.Errorf("workspace lost: %s", got)
		}
		selected := "The owner selected this conversation's project directory: "
		if directory == "" {
			if strings.Contains(got, selected) {
				t.Errorf("project leaked: %s", got)
			}
		} else if !strings.Contains(got, selected+strconv.Quote(directory)) {
			t.Errorf("project missing: %s", got)
		}
	}
}
