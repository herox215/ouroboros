package history

import (
	"testing"
)

func TestDirectory(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	if _, ok, err := a.Directory("owner/s1"); err != nil || ok {
		t.Fatalf("unset %v, %v", ok, err)
	}
	if err := a.SetDirectory("invalid", "/project"); err == nil {
		t.Fatal("accepted invalid id")
	}
	for id, value := range map[string]string{"owner/s1": "/project", "owner/empty": ""} {
		if err := a.SetDirectory(id, value); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	a = Open(dir)
	defer a.Close()
	for id, want := range map[string]string{"owner/s1": "/project", "owner/empty": ""} {
		if got, ok, err := a.Directory(id); err != nil || !ok || got != want {
			t.Fatalf("%s: %q, %v, %v", id, got, ok, err)
		}
	}
	if err := a.Append(Who{Session: "s1"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.Directory("owner/s1"); err != nil || ok {
		t.Fatalf("deleted %v, %v", ok, err)
	}
}
