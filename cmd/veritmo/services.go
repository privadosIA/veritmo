package main

import (
	"path/filepath"

	"veritmo/internal/platform/musicsvc"
)

// musicServices says where the .NET services of the music generators and CLAP are (bin/services, put there by
// scripts/get-services.sh) and where their pid files live (next to the database).
func musicServices(dbPath string) *musicsvc.ServiceHost {
	bin, err := filepath.Abs("bin")
	if err != nil {
		return nil
	}
	state, err := filepath.Abs(filepath.Join(filepath.Dir(dbPath), "services"))
	if err != nil {
		return nil
	}
	return &musicsvc.ServiceHost{Dir: filepath.Join(bin, "services"), StateDir: state, BinDir: bin}
}
