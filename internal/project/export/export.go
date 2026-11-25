// Package export implements the images-mode export phase: it places each
// rendered PNG into the stable out/<project>/ layout, writes the mandatory
// manifest.json that makes every image reproducible, and optionally builds a
// contact sheet via ffmpeg's tile filter (see openspec specs/export).
package export

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"veritmo/internal/platform/fsutil"
	"veritmo/internal/platform/runner"
	"veritmo/internal/project/config"
)

// FFmpegBinary is the executable used for the optional contact sheet.
const FFmpegBinary = "ffmpeg"

// Entry is one image record in manifest.json. Together with the model,
// resolution, sampler and steps it carries everything a consumer needs to
// re-invoke sd and reproduce the identical PNG.
type Entry struct {
	SceneID        string `json:"scene_id"`
	Variant        int    `json:"variant"`
	EffectiveSeed  int64  `json:"effective_seed"`
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt"`
	Model          string `json:"model"`
	// ModelSet is recorded instead of Model when a model set rendered the image.
	ModelSet   *config.ModelSet `json:"model_set,omitempty"`
	Resolution [2]int           `json:"resolution"`
	Sampler    string           `json:"sampler"`
	Steps      int              `json:"steps"`
	File       string           `json:"file"`
	// InitImage and Strength are set when the image was generated from a
	// starting picture (img2img).
	InitImage string   `json:"init_image,omitempty"`
	Strength  *float64 `json:"strength,omitempty"`
}

// Manifest is the top-level manifest.json object.
type Manifest struct {
	Project     string  `json:"project"`
	Mode        string  `json:"mode"`
	GeneratedAt string  `json:"generated_at"`
	Images      []Entry `json:"images"`
}

// Item pairs a rendered PNG in the cache area (Src) with its manifest entry;
// Entry.File is the final exported path under out/<project>/. Dst, when
// non-empty, overrides where the copy is written on disk (used when the
// pipeline runs against a project directory other than the process cwd)
// while Entry.File keeps the project-relative path recorded in the manifest.
type Item struct {
	Src   string
	Dst   string
	Entry Entry
}

// Run executes the export phase: it rejects colliding destination paths
// before touching any file, copies every PNG into place, and writes
// manifest.json atomically. items must be ordered by scene order then
// variant index; now supplies the manifest timestamp (time.Now when nil).
//
// The optional contact sheet is NOT built here: callers invoke ContactSheet
// separately after Run succeeds, so a sheet failure never invalidates the
// already-exported PNGs or the manifest.
func Run(dir, project string, items []Item, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}

	// Reject destination collisions before any file is created or truncated.
	seen := make(map[string]string, len(items)) // dst -> scene id
	for _, it := range items {
		if other, dup := seen[it.Entry.File]; dup {
			return fmt.Errorf("export: scenes %q and %q both resolve to output path %q", other, it.Entry.SceneID, it.Entry.File)
		}
		seen[it.Entry.File] = it.Entry.SceneID
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("export: mkdir %s: %w", dir, err)
	}

	if err := CopyItems(items); err != nil {
		return err
	}
	manifest := Manifest{
		Project:     project,
		Mode:        "images",
		GeneratedAt: now().UTC().Format(time.RFC3339),
		Images:      make([]Entry, 0, len(items)),
	}
	for _, it := range items {
		manifest.Images = append(manifest.Images, it.Entry)
	}

	if err := WriteManifest(filepath.Join(dir, "manifest.json"), manifest); err != nil {
		return err
	}
	return nil
}

// CopyItems copies each item's rendered PNG from the cache area to its
// final exported path, creating intermediate directories as needed.
func CopyItems(items []Item) error {
	for _, it := range items {
		dst := it.Dst
		if dst == "" {
			dst = it.Entry.File
		}
		if err := fsutil.CopyFile(it.Src, dst); err != nil {
			return fmt.Errorf("export: scene %q variant %d: %w", it.Entry.SceneID, it.Entry.Variant, err)
		}
	}
	return nil
}

// WriteManifest writes m as JSON atomically (write to a temporary file in
// the same directory, then rename) so a concurrent reader never observes a
// partial file.
func WriteManifest(path string, m Manifest) error {
	return writeJSONAtomic(path, m)
}

// writeJSONAtomic marshals v as indented JSON and replaces path atomically.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("export: marshal manifest: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("export: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("export: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// TileLayout returns the contact-sheet grid for n images: columns is
// ceil(sqrt(n)) and rows grows to fit, keeping the sheet near-square.
func TileLayout(n int) (cols, rows int) {
	cols = int(math.Ceil(math.Sqrt(float64(n))))
	rows = (n + cols - 1) / cols
	return cols, rows
}

// ContactSheetArgs builds the exact ffmpeg argument list that tiles the
// given PNGs into one sheet at outPath.
func ContactSheetArgs(pngs []string, outPath string) []string {
	args := []string{"-y"}
	for _, p := range pngs {
		args = append(args, "-i", p)
	}
	cols, rows := TileLayout(len(pngs))
	filter := fmt.Sprintf("concat=n=%d,tile=%dx%d", len(pngs), cols, rows)
	args = append(args, "-filter_complex", filter, outPath)
	return args
}

// ContactSheet assembles out/<project>/contact_sheet.png from the exported
// PNGs via ffmpeg's tile filter. A failure here leaves the already-exported
// PNGs and manifest.json intact on disk; the caller reports the error and
// exits non-zero.
func ContactSheet(ctx context.Context, r runner.CommandRunner, pngs []string, outPath string) error {
	if len(pngs) == 0 {
		return fmt.Errorf("export: contact sheet: no images to tile")
	}
	if _, err := r.Run(ctx, FFmpegBinary, ContactSheetArgs(pngs, outPath)); err != nil {
		return fmt.Errorf("export: contact sheet: %w", err)
	}
	return nil
}
