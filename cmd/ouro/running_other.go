//go:build !linux

package main

// runningImage is the image of the running binary exe, read by its path: no
// other name for it stays the running image here.
func runningImage(exe string) string { return exe }
