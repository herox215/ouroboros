package cli_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/home"
	"ouroboros/internal/openai"
	"ouroboros/internal/plugins/base"
	"ouroboros/plugin"
)

func TestOwnerChatMessageSource(t *testing.T) {
	var sources []string
	commands := plugin.New("capture", func(ctx *plugin.Context, _ struct{}) error {
		for _, name := range []string{"chat", "serve"} {
			plugin.Add(ctx, plugin.Command{Name: name, Run: func(ctx context.Context, _ []string) error {
				sources = append(sources, openai.MessageSourceFrom(ctx).Interface)
				return nil
			}})
		}
		return nil
	})
	app, err := cli.NewWith(base.Plugin(home.Dir(t.TempDir()), plugin.Terminal{In: strings.NewReader(""), Out: io.Discard}), commands)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, args := range [][]string{{"chat", "hello"}, {"chat"}, {"serve"}} {
		if err := app.Run(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(sources, ",") != "cli,cli," {
		t.Fatalf("sources: %q", sources)
	}
	if got := openai.MessageSourceFrom(ctx).Interface; got != "" {
		t.Fatalf("base source = %q", got)
	}
}
