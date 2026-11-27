package cache

// track.go: the cache of the video track (spec cache, "A layer, a track item and the stack have their own hashes"). An item of the track, the
// joined track and the stack each keep a hash of what they are made from and the file they made, in the manifest, under a key of their own
// ("track:<id>", "track:_join" and "stack"): the layers of the scenes are cached as scenes and know nothing of the track under them.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// TrackItemKey is the manifest key of the item of the track with the id (see also TrackJoinKey and StackKey).
func TrackItemKey(id string) string { return "track:" + id }

// TrackJoinKey is the manifest key of the joined track.
const TrackJoinKey = "track:_join"

// StackKey is the manifest key of the stack.
const StackKey = "stack"

// IsTrackKey says whether a manifest key is one of the track's (and not a scene's).
func IsTrackKey(key string) bool { return strings.HasPrefix(key, "track:") || key == StackKey }

// trackRevision changes when the way an item, the track or the stack is built changes, so that what was made before is made again.
const trackRevision = "track-v2"

// TrackHash is the hash of what a track item, the joined track or the stack is made from: the parts in order.
func TrackHash(parts ...string) string {
	h := sha256.New()
	h.Write([]byte(trackRevision))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DecideTrack reports whether an item, the track or the stack is a cache hit: the stored hash matches and the file it made is still on disk.
// base is the project directory the stored relative path lives in.
func DecideTrack(m *Manifest, base, key, hash string, force bool) bool {
	if force {
		return false
	}
	e, ok := m.Scenes[key]
	if !ok || e.ClipHash != hash || e.Mp4Path == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(base, e.Mp4Path))
	return err == nil
}

// UpdateTrack records what an item, the track or the stack made.
func (m *Manifest) UpdateTrack(key, hash, path string) {
	m.UpdateEntry(key, SceneEntry{ClipHash: hash, Mp4Path: path})
}
