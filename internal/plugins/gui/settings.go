package gui

// ThreadWindow is how long the thread of the selected profile may hand a
// message to a session after its last one, as the configuration has it, or
// the default.
func (w *Window) ThreadWindow(profile int) (string, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return "", err
	}
	return p.owner.ThreadWindow()
}

// SetThreadWindow sets how long the thread of the selected profile may hand a
// message to a session after its last one: a duration like "3h". It returns
// from when it holds.
func (w *Window) SetThreadWindow(profile int, value string) (string, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return "", err
	}
	return p.owner.SetThreadWindow(value)
}
