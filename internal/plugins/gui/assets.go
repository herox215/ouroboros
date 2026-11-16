package gui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sync"
)

// The window of ouro, built from web/ into frontend/: see "make frontend" in
// the Makefile. The page of the thread in a browser shows the same app.js and
// app.css, with the host it needs instead of Wails: see webpage/.
//
//go:embed frontend
var frontendAssets embed.FS

//go:embed webpage
var webpageAssets embed.FS

// frontendFiles are the files of the window: index.html, app.js, app.css.
func frontendFiles() (fs.FS, error) { return fs.Sub(frontendAssets, "frontend") }

// webpageFiles are the files of the page: the shell that hosts the window in a
// browser, and what makes it an app on a phone.
func webpageFiles() (fs.FS, error) { return fs.Sub(webpageAssets, "webpage") }

// assetVersion names the build of the page this binary serves. A page that
// runs asks after it when it reaches the node again, and loads itself anew
// when the node names another one: a rebuild installs another frontend, and
// the window that stayed open would otherwise show the one of the build
// before it until someone reloads it by hand. An empty name says that the
// files of the page are not readable, and then no page compares anything.
var assetVersion = sync.OnceValue(func() string {
	fsys, err := frontendFiles()
	if err != nil {
		return ""
	}
	sum := sha256.New()
	for _, name := range []string{"app.js", "app.css"} {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return ""
		}
		sum.Write(body)
	}
	return hex.EncodeToString(sum.Sum(nil))[:12]
})
