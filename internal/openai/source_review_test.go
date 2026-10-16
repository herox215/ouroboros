package openai

import "testing"

func TestWithoutMessageSource(t *testing.T) {
	parent := WithMessageSource(t.Context(), MessageSource{Interface: "gui"})
	cleared := WithoutMessageSource(parent)
	if got := MessageSourceFrom(cleared); got != (MessageSource{}) {
		t.Fatalf("source not cleared: %+v", got)
	}
	if got := MessageSourceFrom(parent); got.Interface != "gui" {
		t.Fatalf("parent changed: %+v", got)
	}
	if got := MessageSourceFrom(WithMessageSource(cleared, MessageSource{Interface: "cli"})); got.Interface != "cli" {
		t.Fatalf("cannot set new source: %+v", got)
	}
}
