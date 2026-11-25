package models

import "veritmo/internal/platform/edition"

// The tests talk to fake servers; the public edition accepts only listed hosts.
func init() {
	edition.AllowProviderHostsForTest("127.0.0.1", "localhost", "api.example.com", "ollama.example.com", "other.example.com", "moved.example.com", "example.com", "192.168.1.50", "192.168.1.60", "10.1.1.1")
}
