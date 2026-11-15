package home

// Pictures is the directory in which a profile keeps the pictures its agents
// show the owner in the window, named after their content. The window serves
// them there, and they stay past the answers they were shown in; nothing else
// reads them.
func (d Dir) Pictures() string { return d.path("pictures") }
