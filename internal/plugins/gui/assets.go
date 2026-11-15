package gui

import (
	"embed"
	"io/fs"
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
