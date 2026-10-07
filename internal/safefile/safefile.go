// Package safefile keeps files whole: Write replaces a file in one step, so
// that a crash never leaves it half written, and Lock keeps other processes
// out while a file is read, changed and written back.
package safefile

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Write replaces the file at path by one that holds data and has the mode
// perm. The data is written to a temporary file next to path, which is then
// renamed over it: readers see the old content or the new, never a part.
func Write(path string, data []byte, perm fs.FileMode) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(f.Name())
		}
	}()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync() // the content has to be on disk before it gets the name
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err == nil {
		syncDir(dir)
	}
	return err
}

// syncDir makes the new name of a file survive a crash. Not every file
// system can do that, so failing is not an error. Tests replace it.
var syncDir = func(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// flock locks a file; tests replace it to make locking fail.
var flock = syscall.Flock

// Lock takes the exclusive lock on the file at path, which is created if it
// is missing, and waits as long as another process or goroutine holds it.
// The returned function gives the lock back.
//
// Lock a file of its own, not the one that is replaced with Write: the lock
// belongs to the file, not to its name.
func Lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing the file releases the lock
}
