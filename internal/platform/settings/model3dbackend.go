package settings

// model3dbackend.go: how the 3D generator (TripoSR) runs on this machine, the same two ways as the music generators (see musicbackend.go).

// Model3DBackend is how TripoSR runs here: the saved backend, else the .NET service. MODEL3D_BACKEND is not read: the choice is a setting.
func Model3DBackend() string {
	if Load().Model3DBackend == MusicBackendDocker {
		return MusicBackendDocker
	}
	return MusicBackendDotnet
}

// Model3DBackendSource says where Model3DBackend gets the backend from: FromSetting when one is saved, FromDefault otherwise.
func Model3DBackendSource() string {
	if Load().Model3DBackend != "" {
		return FromSetting
	}
	return FromDefault
}

// SetModel3DBackend saves the backend of the 3D generator (already validated, see ValidateMusicBackend); an empty one removes the setting.
func SetModel3DBackend(backend string) error {
	mu.Lock()
	defer mu.Unlock()
	f, rerr := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if rerr != nil {
		return rerr
	}
	f.Model3DBackend = backend
	return Save(f)
}
