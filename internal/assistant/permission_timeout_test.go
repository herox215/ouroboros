package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
)

func TestPermissionTimeout(t *testing.T) {
	for _, answer := range []string{"", AllowOnce, AllowAlways} {
		t.Run(answer, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var kept []config.Call
				tool, runs := gated(t, "delete", config.Config{}, &kept)
				ctx := agent.WithOwner(context.Background(), func(ctx context.Context, _ json.RawMessage) (string, error) {
					<-ctx.Done()
					// A late answer must not grant permission, even if the callback
					// returns it instead of the context error.
					return answer, nil
				})
				start := time.Now()
				_, err := tool.Run(ctx, json.RawMessage(`{}`))
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "not allowed") {
					t.Fatalf("timeout: %v", err)
				}
				if elapsed := time.Since(start); elapsed != 30*time.Second {
					t.Fatalf("waited %s, want 30s", elapsed)
				}
				if *runs != 0 || len(kept) != 0 || ctx.Err() != nil {
					t.Fatalf("runs=%d, kept=%v, parent error=%v", *runs, kept, ctx.Err())
				}
			})
		})
	}
}

// A rebuild approval stays open past the former 30-second limit. Waiting is
// not approval: refusal still runs nothing, and only AllowAlways is kept.
func TestRebuildPermissionWaitsForOwner(t *testing.T) {
	for _, answer := range []string{AllowOnce, AllowAlways, AllowNot} {
		t.Run(answer, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var kept []config.Call
				tool, runs := gated(t, ToolRebuild, config.Config{}, &kept)
				ctx := agent.WithOwner(context.Background(), func(ctx context.Context, _ json.RawMessage) (string, error) {
					time.Sleep(102 * time.Second)
					if *runs != 0 || len(kept) != 0 || ctx.Err() != nil {
						t.Fatalf("before answer: runs=%d, kept=%v, error=%v", *runs, kept, ctx.Err())
					}
					return answer, nil
				})
				got, err := tool.Run(ctx, json.RawMessage(`{}`))
				if answer == AllowNot {
					if err == nil || *runs != 0 || len(kept) != 0 {
						t.Fatalf("refused: %q, %v, runs=%d, kept=%v", got, err, *runs, kept)
					}
					return
				}
				if err != nil || got != "done" || *runs != 1 {
					t.Fatalf("approved: %q, %v, runs=%d", got, err, *runs)
				}
				if (len(kept) == 1) != (answer == AllowAlways) {
					t.Fatalf("%s: kept=%v", answer, kept)
				}
			})
		})
	}
}

func TestPermissionKeepsEarlierCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			parent, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			want := context.DeadlineExceeded
			if !deadline {
				cancel()
				want = context.Canceled
			}
			ctx := agent.WithOwner(parent, func(ctx context.Context, _ json.RawMessage) (string, error) {
				<-ctx.Done()
				return AllowAlways, nil // cancellation wins even over a late approval
			})
			var kept []config.Call
			tool, runs := gated(t, ToolRebuild, config.Config{}, &kept)
			if _, err := tool.Run(ctx, json.RawMessage(`{}`)); !errors.Is(err, want) || *runs != 0 || len(kept) != 0 {
				t.Fatalf("earlier cancellation: %v, runs=%d", err, *runs)
			}
		})
	}
}

func TestPermissionTimeoutDoesNotLimitTool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tool := agent.Tool{Name: ToolRebuild, Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
			time.Sleep(time.Minute)
			return "done", ctx.Err()
		}}
		guarded := (Builder{}).permitted(config.Config{}, []agent.Tool{tool})[0]
		ctx := agent.WithOwner(context.Background(), func(ctx context.Context, _ json.RawMessage) (string, error) {
			time.Sleep(time.Second)
			return AllowOnce, ctx.Err()
		})
		if got, err := guarded.Run(ctx, json.RawMessage(`{}`)); err != nil || got != "done" {
			t.Fatalf("permitted tool: %q, %v", got, err)
		}
	})
}

func TestUserQuestionIsNotAPermissionWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := config.Config{Tools: &config.Tools{Ask: &[]config.Rule{{Match: ".*"}}}}
		tool := (Builder{}).permitted(cfg, []agent.Tool{userQuestion()})[0]
		ctx := agent.WithOwner(context.Background(), func(ctx context.Context, _ json.RawMessage) (string, error) {
			time.Sleep(time.Minute)
			return "answer", ctx.Err()
		})
		if got, err := tool.Run(ctx, json.RawMessage(`{"question":"Which one?"}`)); err != nil || got != "answer" {
			t.Fatalf("ordinary question: %q, %v", got, err)
		}
	})
}
