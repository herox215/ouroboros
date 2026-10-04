package a2a

import (
	"sync"
	"time"
)

// What strangers can make a node do is limited by the address they come from:
// anyone can make up a key, and every message of an unknown key is written to
// the contacts as a connection request.
const (
	// strangerWindow is the time in which strangerLimit messages of unknown
	// keys are taken from one address. Further ones are refused with
	// CodeTooManyRequests and leave no request.
	strangerWindow = time.Minute
	strangerLimit  = 10
	// maxStrangerAddrs is how many addresses are counted per strangerWindow.
	// Unknown keys from any further address are refused until it is over.
	maxStrangerAddrs = 1024
)

// limiter counts the messages of unknown keys per address within the current
// strangerWindow. With the window the counts are dropped as a whole, so it
// never holds more than maxStrangerAddrs addresses. The zero value is ready.
type limiter struct {
	mu    sync.Mutex
	since time.Time
	count map[string]int
}

// allow reports whether a message of an unknown key from host is taken at
// the time now, and counts it if so.
func (l *limiter) allow(host string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.count == nil || now.Sub(l.since) >= strangerWindow {
		l.since, l.count = now, map[string]int{}
	}
	n, counted := l.count[host]
	if n >= strangerLimit || !counted && len(l.count) >= maxStrangerAddrs {
		return false
	}
	l.count[host] = n + 1
	return true
}
