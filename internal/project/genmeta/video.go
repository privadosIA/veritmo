package genmeta

import (
	"context"
	"encoding/json"

	"veritmo/internal/platform/runner"
)

// ReadVideoTags returns the format tags of a video file through ffprobe. An
// ffprobe that is missing or fails yields no tags and an error the caller may
// ignore: metadata is optional.
func ReadVideoTags(ctx context.Context, r runner.CommandRunner, path string) (map[string]string, error) {
	out, err := r.Run(ctx, "ffprobe", []string{"-v", "error", "-show_entries", "format_tags", "-of", "json", path})
	if err != nil {
		return nil, err
	}
	var doc struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if len(out) > MaxInspect {
		return nil, nil
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	return doc.Format.Tags, nil
}

// FromVideo reads the metadata of a video: its container tags.
func FromVideo(ctx context.Context, r runner.CommandRunner, path string) Metadata {
	tags, err := ReadVideoTags(ctx, r, path)
	if err != nil || len(tags) == 0 {
		return Metadata{}
	}
	return FromVideoTags(tags)
}
