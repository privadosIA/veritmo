package settings

import (
	"errors"
	"strings"
	"time"
)

// LicenceAcceptance is the record of a person's explicit acceptance of a licence with conditions (see package licence): when it was given and the label
// of the client that sent it. The server writes one only when a request carries the acceptance; it never accepts on anyone's behalf.
type LicenceAcceptance struct {
	At     time.Time `json:"at"`
	Client string    `json:"client,omitempty"`
}

// maxClientLen is the longest client label kept.
const maxClientLen = 64

// AcceptLicence records that the licence with this id was accepted, with the label of the client that sent the acceptance. A licence that was
// accepted before keeps its first record.
func AcceptLicence(id, client string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("a licence id is needed")
	}
	client = strings.TrimSpace(client)
	if len(client) > maxClientLen {
		client = client[:maxClientLen]
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := ReadFile() // a source that cannot be read is not "no settings": saving would erase them
	if err != nil {
		return err
	}
	if _, done := f.LicenceAcceptances[id]; done {
		return nil
	}
	if f.LicenceAcceptances == nil {
		f.LicenceAcceptances = map[string]LicenceAcceptance{}
	}
	f.LicenceAcceptances[id] = LicenceAcceptance{At: time.Now().UTC().Truncate(time.Second), Client: client}
	return Save(f)
}

// LicenceAccepted reports whether the licence with this id was accepted on this server.
func LicenceAccepted(id string) bool {
	_, ok := Load().LicenceAcceptances[strings.TrimSpace(id)]
	return ok
}
