package server

// core_catalog.go: the model catalog the server embeds (catalog.json, and catalog_private.json in the private edition;
// see core_catalog_private.go). The families read it through modelcat.Entry and their Host.

import (
	_ "embed"
	"encoding/json"

	"veritmo/internal/server/modelcat"
)

//go:embed catalog.json
var catalogJSON []byte

// The entries of the catalog are described in package modelcat; the server uses them by these names.
type (
	catalogEntry = modelcat.Entry
)

var catalog = func() []catalogEntry {
	var c []catalogEntry
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("server: invalid embedded catalog.json: " + err.Error())
	}
	if len(privateCatalogJSON) > 0 {
		var extra []catalogEntry
		if err := json.Unmarshal(privateCatalogJSON, &extra); err != nil {
			panic("server: invalid embedded catalog_private.json: " + err.Error())
		}
		c = append(c, extra...)
	}
	return c
}()

// Catalog is the model catalog this program embeds, for what runs without a server (the command line).
func Catalog() []modelcat.Entry { return append([]modelcat.Entry(nil), catalog...) }
