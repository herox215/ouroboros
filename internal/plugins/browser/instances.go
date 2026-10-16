package browser

import "sync"

// browserBackends keeps independently configured remote services alive. Owner
// and peer agents can legitimately use different services at the same time.
// Local Chromium remains a single profile-scoped browser outside this registry.
type browserBackends struct {
	mu        sync.Mutex
	instances map[settings]*browser
}

func (r *browserBackends) get(profile string, s settings) *browser {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.instances == nil {
		r.instances = map[settings]*browser{}
	}
	b := r.instances[s]
	if b == nil {
		b = &browser{profile: profile, convs: map[string]*conversation{}}
		r.instances[s] = b
	}
	return b
}

func (r *browserBackends) close() {
	r.mu.Lock()
	instances := r.instances
	r.instances = nil
	r.mu.Unlock()
	for _, b := range instances {
		b.close()
	}
}
