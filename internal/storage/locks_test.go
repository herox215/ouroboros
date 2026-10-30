package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Open, called again in a process whose pool holds the database, leaves the
// locks of that pool alone. Once it opened and closed the file, which drops
// every POSIX lock of the process on it; a read of the index by anything else
// in the process, as a search over the directory, dropped those on the index.
// Another process that closed its last connection then took itself for the
// last one and removed the WAL and its index under the pool (#153).
func TestOpenKeepsTheLocksOfThePoolsOfItsProcess(t *testing.T) {
	path := filepath.Join(os.Getenv("OURO_STORAGE_HOLDER"), FileName)
	if dir := os.Getenv("OURO_STORAGE_HOLDER"); dir != "" {
		// The process that holds the database, opens it once more and reads
		// its index as a file.
		db := openTest(t, dir)
		execTest(t, db, "INSERT INTO t VALUES (2)")
		openTest(t, dir).Close()
		if _, err := os.ReadFile(path + "-shm"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".ready", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		execTest(t, db, "INSERT INTO t VALUES (3)")
		return
	}
	dir := t.TempDir()
	path = filepath.Join(dir, FileName)
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, db, "CREATE TABLE t(x); INSERT INTO t VALUES (1)")
	holder := exec.Command(os.Args[0], "-test.run=^TestOpenKeepsTheLocksOfThePoolsOfItsProcess$")
	holder.Env = append(os.Environ(), "OURO_STORAGE_HOLDER="+dir)
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer holder.Wait()
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(path + ".ready"); err == nil {
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("the other process did not open the database")
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + sidecar); err != nil {
			t.Errorf("the %s of a database another process holds is gone: %v", sidecar, err)
		}
	}
}
