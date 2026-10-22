package gui

import (
	"context"
	"crypto/sha256"
	"io"
	"os"
	"runtime"
	"strings"
	"time"
)

// watchExecutable notices installed builds independently of the selected
// session. Poll the path, not an open inode: installation replaces it by rename.
// Errors and the brief gap during replacement leave the old window running.
func watchExecutable(ctx context.Context, changed func(string)) {
	exe, err := executable()
	if err != nil {
		return
	}
	running := exe
	if runtime.GOOS == "linux" && selfImage != "" {
		// /proc/self/exe names the old inode with this suffix if installation
		// won the race with watcher startup. Keep watching the install path.
		exe = strings.TrimSuffix(exe, " (deleted)")
		running = selfImage
	}
	image, err := executableImage(running)
	if err != nil {
		return
	}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	watchImage(ctx, exe, image, ticker.C, changed)
}

// executable and selfImage are where the running build is installed and
// where its image is read, replaced by tests.
var (
	executable = os.Executable
	selfImage  = "/proc/self/exe"
)

// watchInterval is how often the installed build is looked at.
var watchInterval = time.Second

type binaryImage struct {
	info os.FileInfo
	hash [sha256.Size]byte
}

func executableImage(path string) (binaryImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return binaryImage{}, err
	}
	defer f.Close()
	h := sha256.New()
	info, err := f.Stat()
	if err == nil {
		_, err = io.Copy(h, f) // a directory opens, but does not read
	}
	if err != nil {
		return binaryImage{}, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return binaryImage{info: info, hash: sum}, nil
}

func watchImage(ctx context.Context, exe string, image binaryImage, ticks <-chan time.Time, changed func(string)) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			info, err := os.Stat(exe)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				continue
			}
			if os.SameFile(image.info, info) && image.info.Size() == info.Size() && image.info.ModTime().Equal(info.ModTime()) {
				continue
			}
			next, err := executableImage(exe)
			if err != nil {
				continue
			}
			if next.hash != image.hash {
				if ctx.Err() == nil {
					changed(exe)
				}
				return
			}
			image = next // an identical reinstall is not a GUI update
		}
	}
}
