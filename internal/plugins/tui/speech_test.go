package tui

import "ouroboros/internal/speech"

// Owner's speech methods must remain promoted by the plugin backend, without
// adding them to the terminal's required Backend interface.
var _ interface {
	SpeechSettings() (speech.Snapshot, error)
	SaveSpeech(speech.Config) error
	DownloadSpeech() error
	CancelSpeechDownload() error
} = (*ui)(nil)
