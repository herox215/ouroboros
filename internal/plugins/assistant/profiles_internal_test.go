package assistant

import (
	"os"
	"path/filepath"
	"testing"

	"ouroboros/internal/home"
)

// The archivist gets the memories of every profile, the first one's first,
// and none where the profiles cannot be listed.
func TestMemoryProfiles(t *testing.T) {
	root := home.Dir(t.TempDir())
	profiles := memoryProfiles(root)()
	if len(profiles) != 1 || profiles[0].Dir != string(root) {
		t.Fatalf("profiles: %+v", profiles)
	}
	store, err := profiles[0].Store()
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := os.WriteFile(filepath.Join(string(root), "profiles"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if profiles := memoryProfiles(root)(); profiles != nil {
		t.Fatalf("profiles that cannot be listed: %+v", profiles)
	}
}
