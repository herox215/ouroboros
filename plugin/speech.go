package plugin

import (
	"path/filepath"

	"ouroboros/internal/speech"
)

// SpeechSettings reads the profile's speech setup without starting a download.
// Credentials are omitted from its snapshot.
func (o *Owner) SpeechSettings() (speech.Snapshot, error) {
	return speech.New(filepath.Join(string(o.home), "speech")).Snapshot()
}

// SaveSpeech updates recognition settings. Models are downloaded only on request.
func (o *Owner) SaveSpeech(change speech.Config) error {
	return speech.New(filepath.Join(string(o.home), "speech")).Save(change)
}

// DownloadSpeech prepares the saved local runtime and model asynchronously.
func (o *Owner) DownloadSpeech() error {
	return speech.New(filepath.Join(string(o.home), "speech")).StartDownload()
}

// CancelSpeechDownload requests cancellation of the profile's current setup.
func (o *Owner) CancelSpeechDownload() error {
	return speech.New(filepath.Join(string(o.home), "speech")).CancelDownload()
}
