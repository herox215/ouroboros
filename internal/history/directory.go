package history

// SetDirectory keeps the project directory of a conversation, including an
// explicit empty reset. The conversation needs not be archived yet.
func (a *Archive) SetDirectory(id, directory string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	return a.setBeside(a.id(path), directorySuffix, directory)
}

// Directory returns the saved project directory and whether it was set.
func (a *Archive) Directory(id string) (string, bool, error) {
	return a.beside(id, directorySuffix)
}
