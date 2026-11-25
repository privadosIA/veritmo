package edition

import "testing"

func TestVersionLine(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	for _, c := range []struct{ set, number, line string }{
		{"v0.1.0", "0.1.0", "veritmo 0.1.0 (" + Name + " edition)"},
		{"0.1.0", "0.1.0", "veritmo 0.1.0 (" + Name + " edition)"},
		{"v0.1.0-3-gabc1234", "0.1.0-3-gabc1234", "veritmo 0.1.0-3-gabc1234 (" + Name + " edition)"},
		{"dev", "dev", "veritmo dev (" + Name + " edition)"},
		{"", "dev", "veritmo dev (" + Name + " edition)"},
		{"  v1.2.3\n", "1.2.3", "veritmo 1.2.3 (" + Name + " edition)"},
	} {
		Version = c.set
		if got := VersionNumber(); got != c.number {
			t.Errorf("Version %q: number %q, want %q", c.set, got, c.number)
		}
		if got := VersionLine(); got != c.line {
			t.Errorf("Version %q: line %q, want %q", c.set, got, c.line)
		}
	}
}
