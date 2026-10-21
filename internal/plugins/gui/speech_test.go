package gui

import (
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/speech"
)

func TestWindowSpeechSettings(t *testing.T) {
	w, _ := modelWindow(t)
	initial, err := w.SpeechSettings(0)
	if err != nil {
		t.Fatal(err)
	}
	change := speech.Config{Provider: "remote", Model: "whisper-1", Endpoint: "https://example.invalid/v1", APIKey: "speech-test-secret", Language: "de"}
	if err := w.SaveSpeech(1, change); err != nil {
		t.Fatal(err)
	}
	got, err := w.SpeechSettings(1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Provider != "remote" || got.Config.Model != change.Model || got.Config.Endpoint != change.Endpoint || got.Config.Language != "de" || !got.HasKey || got.Download.Active {
		t.Fatalf("saved speech: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), change.APIKey) || got.Config.APIKey != "" {
		t.Fatal("speech snapshot exposed API key")
	}
	change.APIKey = ""
	change.Language = "en"
	if err := w.SaveSpeech(1, change); err != nil {
		t.Fatal(err)
	}
	got, err = w.SpeechSettings(1)
	if err != nil || !got.HasKey || got.Config.Language != "en" {
		t.Fatalf("blank key did not preserve saved key: %+v, %v", got, err)
	}
	other, err := w.SpeechSettings(0)
	if err != nil || other.Config != initial.Config || other.HasKey != initial.HasKey || other.Download.Active {
		t.Fatalf("other profile changed: %+v, %v", other, err)
	}
	// A remote configuration cannot trigger local setup; no network fixture is
	// needed to verify that the bridge forwards the backend rejection.
	if err := w.DownloadSpeech(1); err == nil {
		t.Fatal("remote download accepted")
	}
	if err := w.CancelSpeechDownload(1); err != nil {
		t.Fatal(err)
	}
}

func TestWindowSpeechValidation(t *testing.T) {
	w, _ := modelWindow(t)
	for _, index := range []int{-1, 2} {
		if _, err := w.SpeechSettings(index); err == nil {
			t.Fatal("invalid speech profile accepted")
		}
		if err := w.SaveSpeech(index, speech.Config{}); err == nil {
			t.Fatal("invalid save profile accepted")
		}
		if err := w.DownloadSpeech(index); err == nil {
			t.Fatal("invalid download profile accepted")
		}
		if err := w.CancelSpeechDownload(index); err == nil {
			t.Fatal("invalid cancellation profile accepted")
		}
	}
	if err := w.SaveSpeech(0, speech.Config{Provider: "invalid"}); err == nil {
		t.Fatal("invalid speech configuration accepted")
	}
	w.profiles[0].owner = nil
	if _, err := w.SpeechSettings(0); err == nil {
		t.Fatal("missing owner accepted")
	}
	if err := w.SaveSpeech(0, speech.Config{}); err == nil {
		t.Fatal("save without owner accepted")
	}
	if err := w.DownloadSpeech(0); err == nil {
		t.Fatal("download without owner accepted")
	}
	if err := w.CancelSpeechDownload(0); err == nil {
		t.Fatal("cancellation without owner accepted")
	}
	w.Close()
	if _, err := w.SpeechSettings(1); err == nil {
		t.Fatal("closed window accepted")
	}
}
