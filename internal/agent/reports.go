package agent

import (
	"sync"
)

// reports are tool results that become available while other work goes on.
// Their text is made only when delivered, so a tool may suppress a result
// already returned by an explicit wait or inspection.
type reports struct {
	mu      sync.Mutex
	pending []report
}

type report struct {
	done <-chan struct{}
	text func() string
}

// Watch delivers text once done closes, at the next boundary between rounds.
// It does not keep the answer alive: long-running jobs still end with it.
// text must not block and may return "" if the result was already delivered.
func (a *Answer) Watch(done <-chan struct{}, text func() string) {
	a.reports.mu.Lock()
	defer a.reports.mu.Unlock()
	a.reports.pending = append(a.reports.pending, report{done, text})
}

func (a *Answer) reported() []string {
	a.reports.mu.Lock()
	defer a.reports.mu.Unlock()
	var texts []string
	pending := a.reports.pending[:0]
	for _, r := range a.reports.pending {
		select {
		case <-r.done:
			if text := r.text(); text != "" {
				texts = append(texts, text)
			}
		default:
			pending = append(pending, r)
		}
	}
	clear(a.reports.pending[len(pending):])
	a.reports.pending = pending
	return texts
}
