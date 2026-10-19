package history

import (
	"strings"
	"testing"
)

// References to past design decisions need evidence, not blanket agreement.
func TestSearchRequiresCheckingEarlierAgreements(t *testing.T) {
	h := &archive{}
	for _, tool := range h.tools() {
		if tool.Name != Search {
			continue
		}
		for _, want := range []string{
			"including UI or dialog design",
			"search the conversation history before confirming specific details as agreed",
			"Use scope all for the owner's earlier conversations",
			"read the surrounding records with history_read",
			"distinguish a proposal from an agreement",
			"history is unavailable or inconclusive, state what is unverified",
			"ask a focused question only if needed",
		} {
			if !strings.Contains(tool.Description, want) {
				t.Errorf("search description lacks %q", want)
			}
		}
		return
	}
	t.Fatal("history search tool missing")
}
