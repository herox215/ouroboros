package main

// runningImage is the image of the running binary exe: /proc/self/exe stays
// it even after another launch replaces exe.
func runningImage(string) string { return "/proc/self/exe" }
