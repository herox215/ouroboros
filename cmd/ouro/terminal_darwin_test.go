package main

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// getTermios reads the settings of a terminal.
const getTermios = syscall.TIOCGETA

// unlockTerminal makes the user's end of the pseudo-terminal behind keyboard
// usable and returns its name.
func unlockTerminal(keyboard *os.File) (string, error) {
	if err := ioctl(keyboard, syscall.TIOCPTYGRANT, nil); err != nil {
		return "", err
	}
	if err := ioctl(keyboard, syscall.TIOCPTYUNLK, nil); err != nil {
		return "", err
	}
	name := make([]byte, 128) // the size TIOCPTYGNAME is defined with
	if err := ioctl(keyboard, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		return "", err
	}
	return string(name[:max(bytes.IndexByte(name, 0), 0)]), nil
}
