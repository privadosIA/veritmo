package cache_test

import (
	"fmt"

	"veritmo/internal/platform/cache"
)

// A picture is found again in the cache when every parameter that draws it is the same: change one (here the seed)
// and the hash, which is the name of the cached file, changes with it.
func ExampleImageHash() {
	p := cache.ImageParams{Prompt: "a bench in a park", Seed: 7, Model: "models/m.safetensors", Resolution: "1024x576", Sampler: "euler", Steps: 20}
	same := p
	other := p
	other.Seed = 8

	fmt.Println(len(cache.ImageHash(p)), "hex characters")
	fmt.Println("same parameters, same hash:", cache.ImageHash(p) == cache.ImageHash(same))
	fmt.Println("another seed, another hash:", cache.ImageHash(p) != cache.ImageHash(other))
	// Output:
	// 64 hex characters
	// same parameters, same hash: true
	// another seed, another hash: true
}
