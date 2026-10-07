package llm

import (
	"os/exec"
	"runtime"
)

// opener is the program that shows a URL in the browser of the user, by
// system; tests replace it.
var opener = map[string][]string{
	"darwin":  {"open"},
	"windows": {"rundll32", "url.dll,FileProtocolHandler"},
}

// Browse shows url in the browser of the user. It fails where there is none
// to start, as on a machine without a desktop: the URL is shown to the owner
// either way. Tests replace it.
var Browse = func(url string) error {
	command := opener[runtime.GOOS]
	if command == nil {
		command = []string{"xdg-open"}
	}
	cmd := exec.Command(command[0], append(command[1:], url)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // not waited for: the browser may stay open
	return nil
}
