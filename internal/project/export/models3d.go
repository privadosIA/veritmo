package export

import (
	"encoding/json"
	"fmt"
	"os"
)

// Models3DMode is the Mode of the manifest of a models3d-mode project.
const Models3DMode = "models3d"

// Models3DEntry is one model a models3d-mode run made: its entry, its file, and what made it. Key is the hash of what decides it (the picture, the
// engine and its version, the detail, the seed): an entry whose key is unchanged and whose file is still there is not made again.
type Models3DEntry struct {
	// Entry is the id of the entry of models: of the project; Variant is 0-based.
	Entry   string `json:"entry"`
	Variant int    `json:"variant"`
	// Name is the name of the model in the library and File its path in the project (out/<project>/<id>.glb).
	Name string `json:"name"`
	File string `json:"file"`
	// Prompt is the text the picture was made from ("" for an entry that brought its own picture, named in Picture).
	Prompt  string `json:"prompt,omitempty"`
	Picture string `json:"picture,omitempty"`
	Engine  string `json:"engine"`
	// EngineVersion is what the service reported; Detail the detail it was made at (lower than asked when the first had too many triangles).
	EngineVersion string `json:"engine_version"`
	Detail        string `json:"detail"`
	Seed          int64  `json:"seed"`
	Triangles     int    `json:"triangles"`
	// Seconds is the time the model took (the picture and the service).
	Seconds float64 `json:"seconds"`
	Adult   bool    `json:"adult,omitempty"`
	Key     string  `json:"key"`
}

// Models3DManifest is the manifest.json of a models3d-mode project.
type Models3DManifest struct {
	Project     string          `json:"project"`
	Mode        string          `json:"mode"`
	GeneratedAt string          `json:"generated_at"`
	Models      []Models3DEntry `json:"models"`
}

// WriteModels3DManifest writes m atomically.
func WriteModels3DManifest(path string, m Models3DManifest) error {
	return writeJSONAtomic(path, m)
}

// ReadModels3DManifest reads a models3d manifest.json. It fails for a file that is missing, unreadable or not a models3d manifest, so a caller
// that only wants to reuse an earlier one can treat any error as "nothing to reuse".
func ReadModels3DManifest(path string) (Models3DManifest, error) {
	var m Models3DManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("export: parse %s: %w", path, err)
	}
	if m.Mode != Models3DMode {
		return m, fmt.Errorf("export: %s is not a models3d manifest (mode %q)", path, m.Mode)
	}
	return m, nil
}
