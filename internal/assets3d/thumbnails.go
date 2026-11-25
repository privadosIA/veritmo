package assets3d

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoPreview is a model that has no picture from its pack and cannot be drawn here (there is no way to draw it).
var ErrNoPreview = errors.New("the model has no preview picture and cannot be drawn")

// thumbnailWidth is the side of a drawn thumbnail.
const thumbnailWidth = 256

// thumbDescription is the scene a thumbnail is drawn of: the model on its own, its longest dimension 1 m whatever its proportions (a
// flat or wide piece fits as a tall one does), lit from two sides, with lights that do not burn the pale flat colours of some kits.
const thumbDescription = `{"version":1,"world":{"color":"#2a2f3a"},"camera":{"lens":50,"keys":[{"t":0,"location":[0,-2.8,0.5],"target":[0,0,0.4]}]},` +
	`"lights":[{"type":"sun","rotation":[55,0,25],"power":1.2},{"type":"area","location":[2,-3,3],"target":[0,0,0.5],"power":120,"size":3}],` +
	`"objects":[{"id":"m","type":"model","file":%q,"size":1,"fit":"largest"}]}`

// Thumbnail is a small picture of a model: the one its pack has, else one drawn with Blender and kept.
func (l *Library) Thumbnail(ctx context.Context, pack, name string) ([]byte, string, error) {
	_, m, err := l.find(pack, name)
	if err != nil {
		return nil, "", err
	}
	if m.Preview != "" {
		if b, err := os.ReadFile(filepath.Join(l.FilesDir(pack), filepath.FromSlash(m.Preview))); err == nil {
			return b, contentTypeOf(m.Preview), nil
		}
	}
	kept := filepath.Join(l.packDir(pack), "thumbs", name+".png")
	if b, err := os.ReadFile(kept); err == nil {
		return b, "image/png", nil
	}
	if l.Preview == nil {
		return nil, "", ErrNoPreview
	}
	tmp, err := os.MkdirTemp("", "thumb-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	res, err := l.CopyModel(pack+"/"+name, tmp, "")
	if err != nil {
		return nil, "", err
	}
	png, err := l.Preview(ctx, tmp, []byte(fmt.Sprintf(thumbDescription, res.Name)), thumbnailWidth)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(kept), 0o755); err == nil {
		_ = os.WriteFile(kept, png, 0o644)
	}
	return png, "image/png", nil
}

func contentTypeOf(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	}
	return "image/png"
}
