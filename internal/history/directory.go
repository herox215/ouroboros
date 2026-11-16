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

// SetAgent keeps which agent of the profile the conversation runs as. The
// conversation needs not be archived yet.
func (a *Archive) SetAgent(id, agent string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	return a.setBeside(a.id(path), agentSuffix, agent)
}

// Agent returns the agent the conversation runs as, and whether it was kept:
// a conversation from before agents were kept has none.
func (a *Archive) Agent(id string) (string, bool, error) {
	return a.beside(id, agentSuffix)
}
