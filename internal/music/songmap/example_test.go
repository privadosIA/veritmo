package songmap_test

import (
	"fmt"

	"veritmo/internal/music/songmap"
)

// A map of a song of 20 seconds at 120 beats a minute whose lyrics have a verse and a chorus: the beats are half a
// second apart and each part of the lyrics is a section with its start and end in seconds.
func ExampleBuild() {
	m := songmap.Build(songmap.Input{
		Duration:      20,
		BPM:           120,
		Lyrics:        "[Verse]\nthe first line\n[Chorus]\nthe second line",
		EstimateWords: true,
	})
	fmt.Printf("%d beats, the second at %.1f s\n", len(m.Beats), m.Beats[1])
	for _, s := range m.Sections {
		fmt.Printf("%s %.1f-%.1f\n", s.Tag, s.Start, s.End)
	}
	// Output:
	// 40 beats, the second at 0.5 s
	// Verse 0.0-9.9
	// Chorus 9.9-20.0
}
