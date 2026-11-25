package settings

import (
	"strings"
	"testing"
)

func TestLicenceAcceptanceIsRecordedOnceWithItsClient(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if LicenceAccepted("stability-community") {
		t.Fatal("nothing was accepted yet")
	}
	if err := AcceptLicence("  ", "x"); err == nil {
		t.Error("an empty id is refused")
	}
	if err := AcceptLicence("stability-community", " model-studio "); err != nil {
		t.Fatal(err)
	}
	if !LicenceAccepted("stability-community") || LicenceAccepted("another") {
		t.Fatal("only that licence is accepted")
	}
	first := Load().LicenceAcceptances["stability-community"]
	if first.Client != "model-studio" || first.At.IsZero() {
		t.Fatalf("record: %+v", first)
	}
	// a second acceptance does not change the first record
	if err := AcceptLicence("stability-community", "other-client"); err != nil {
		t.Fatal(err)
	}
	if again := Load().LicenceAcceptances["stability-community"]; again != first {
		t.Errorf("the first record is kept: %+v, was %+v", again, first)
	}
	// the label is cut, not refused
	if err := AcceptLicence("long", strings.Repeat("x", 200)); err != nil || len(Load().LicenceAcceptances["long"].Client) != maxClientLen {
		t.Errorf("the label is cut to %d: %v", maxClientLen, err)
	}
}
