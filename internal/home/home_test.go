package home

import (
	"testing"

	"ouroboros/internal/config"
)

func TestIdentityIsGeneratedOnce(t *testing.T) {
	d := Dir(t.TempDir())
	if err := d.SaveConfig(config.Config{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	first, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Identity()
	if err != nil || second.Public() != first.Public() {
		t.Fatalf("identity changed: %v", err)
	}
	if cfg, _ := d.Config(); cfg.Name != "alpha" || cfg.PrivateKey != first.Private() {
		t.Fatalf("config %+v", cfg)
	}
}

func TestInvalidIdentity(t *testing.T) {
	d := Dir(t.TempDir())
	d.SaveConfig(config.Config{PrivateKey: "nonsense"})
	if _, err := d.Identity(); err == nil {
		t.Fatal("want error")
	}
}
