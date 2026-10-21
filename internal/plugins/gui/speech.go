package gui

import "ouroboros/internal/speech"

// SpeechSettings reads only the selected profile's public speech snapshot.
func (w *Window) SpeechSettings(profile int) (speech.Snapshot, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return speech.Snapshot{}, err
	}
	return p.owner.SpeechSettings()
}

// SaveSpeech persists settings without starting a download or runtime.
func (w *Window) SaveSpeech(profile int, change speech.Config) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	return p.owner.SaveSpeech(change)
}

// DownloadSpeech explicitly starts setup using the saved configuration.
func (w *Window) DownloadSpeech(profile int) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	return p.owner.DownloadSpeech()
}

// CancelSpeechDownload cancels setup for exactly the selected profile.
func (w *Window) CancelSpeechDownload(profile int) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	return p.owner.CancelSpeechDownload()
}
