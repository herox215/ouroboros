package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// getTermios reads the settings of a terminal.
const getTermios = syscall.TCGETS

// unlockTerminal makes the user's end of the pseudo-terminal behind keyboard
// usable and returns its name.
func unlockTerminal(keyboard *os.File) (string, error) {
	var unlocked, number int32
	if err := ioctl(keyboard, syscall.TIOCSPTLCK, unsafe.Pointer(&unlocked)); err != nil {
		return "", err
	}
	if err := ioctl(keyboard, syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		return "", err
	}
	return fmt.Sprintf("/dev/pts/%d", number), nil
}
