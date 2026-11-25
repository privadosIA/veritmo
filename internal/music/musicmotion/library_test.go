package musicmotion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veritmo/internal/platform/store"
)

func lib() Library { return Library{Builtin: BuiltinLibrary()} }

func TestTheLibraryComesWithCharactersAndBackgrounds(t *testing.T) {
	chars, bgs := lib().Characters(), lib().Backgrounds()
	if len(chars) != 3 || chars[0].Name != "bot" || chars[1].Name != "mascot" || chars[2].Name != "scientist" || len(bgs) != 4 {
		t.Fatalf("%d characters, %d backgrounds", len(chars), len(bgs))
	}
	bot := chars[0]
	if got := strings.Join(bot.PoseNames(), ","); got != "idle,point,wave" || !bot.HasExpression("surprised") || bot.SVG["wave"] == "" {
		t.Fatalf("%+v", bot.CharacterManifest)
	}
	if bgs[0].Name != "night-sky" || bgs[0].SVG == "" || bgs[0].Source != "builtin" {
		t.Fatalf("%+v", bgs[0])
	}
}

func writeChar(t *testing.T, user, name, manifest string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(user, "characters", name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644)
	for f, c := range files {
		os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644)
	}
}

const okSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10" fill="var(--accent)"/></svg>`

func TestACharacterAddedByTheUserShowsUpOnTheNextList(t *testing.T) {
	user := t.TempDir()
	l := Library{Builtin: BuiltinLibrary(), UserDir: user}
	if len(l.Characters()) != 3 {
		t.Fatal("start with three")
	}
	writeChar(t, user, "cat", `{"name":"cat","description":"A cat","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg"},"expressions":["happy"],"default_pose":"idle","default_expression":"happy"}`, map[string]string{"idle.svg": okSVG})
	got := l.Characters()
	if len(got) != 4 || got[0].Name != "bot" || got[1].Name != "cat" || got[1].Source != "user" {
		t.Fatalf("%+v", got)
	}
	if _, ok := l.Character("cat"); !ok {
		t.Fatal("a script must be able to use it")
	}
}

func TestACharacterWithAMissingPoseFileIsLeftOutAndTheOthersStay(t *testing.T) {
	user := t.TempDir()
	writeChar(t, user, "ghost", `{"name":"ghost","description":"x","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg","wave":"wave.svg"},"default_pose":"idle"}`, map[string]string{"idle.svg": okSVG})
	writeChar(t, user, "evil", `{"name":"evil","description":"x","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg"},"default_pose":"idle"}`, map[string]string{"idle.svg": `<svg><script>alert(1)</script></svg>`})
	writeChar(t, user, "remote", `{"name":"remote","description":"x","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg"},"default_pose":"idle"}`, map[string]string{"idle.svg": `<svg><image href="https://x.invalid/a.png"/></svg>`})
	writeChar(t, user, "badname", `{"name":"other","description":"x","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg"},"default_pose":"idle"}`, map[string]string{"idle.svg": okSVG})
	writeChar(t, user, "baddefault", `{"name":"baddefault","description":"x","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg"},"default_pose":"dance"}`, map[string]string{"idle.svg": okSVG})
	got := Library{Builtin: BuiltinLibrary(), UserDir: user}.Characters()
	if len(got) != 3 {
		names := []string{}
		for _, c := range got {
			names = append(names, c.Name)
		}
		t.Fatalf("only the built-in characters should remain: %v", names)
	}
}

func writeItem(t *testing.T, root, folder, name, manifest string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, folder, name)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644)
	for f, c := range files {
		os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644)
	}
}

const catManifest = `{"name":"cat","description":"A cat","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg"},"expressions":["happy"],"default_pose":"idle","default_expression":"happy"}`

func TestTheLibraryOfTheDatabaseKeepsAddsReplacesAndDeletesItems(t *testing.T) {
	st := memStore(t)
	items := DBLibrary(st)
	l := NewLibrary(items)
	if len(l.Characters()) != 3 {
		t.Fatal("start with the three of the program")
	}
	if p := CheckLibraryItem(KindCharacter, "cat", []byte(catManifest), map[string]string{"idle.svg": okSVG}); len(p) != 0 {
		t.Fatal(p)
	}
	if err := items.Put(KindCharacter, "cat", []byte(catManifest), map[string]string{"idle.svg": okSVG}); err != nil {
		t.Fatal(err)
	}
	got := l.Characters()
	if len(got) != 4 || got[1].Name != "cat" || got[1].Source != "user" || got[1].SVG["idle"] != okSVG {
		t.Fatalf("%+v", got)
	}
	// a replacement leaves one set of drawings
	newer := strings.Replace(okSVG, "var(--accent)", "var(--fg)", 1)
	if err := items.Put(KindCharacter, "cat", []byte(catManifest), map[string]string{"idle.svg": newer}); err != nil {
		t.Fatal(err)
	}
	if c, _ := l.Character("cat"); c.SVG["idle"] != newer {
		t.Fatalf("%+v", c)
	}
	var n int64
	st.DB.Model(&libraryFileRow{}).Count(&n)
	if n != 1 {
		t.Fatalf("%d drawings", n)
	}
	// a background
	bgManifest := `{"name":"night","description":"x","version":1,"file":"night.svg"}`
	if err := items.Put(KindBackground, "night", []byte(bgManifest), map[string]string{"night.svg": okSVG}); err != nil {
		t.Fatal(err)
	}
	if b, ok := l.Background("night"); !ok || b.Source != "user" {
		t.Fatalf("%+v %v", b, ok)
	}
	if found, err := items.Delete(KindCharacter, "cat"); err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	if found, _ := items.Delete(KindCharacter, "cat"); found {
		t.Fatal("it was already gone")
	}
	if _, ok := l.Character("cat"); ok {
		t.Fatal("deleted")
	}
	if _, ok := l.Background("night"); !ok {
		t.Fatal("the background of another kind stays")
	}
}

func TestCheckLibraryItemSaysWhatIsWrongWithAPostedItem(t *testing.T) {
	cases := []struct {
		name, kind, manifest string
		files                map[string]string
		want                 string
	}{
		{"script", KindCharacter, catManifest, map[string]string{"idle.svg": `<svg><script>x</script></svg>`}, "script"},
		{"missing pose", KindCharacter, strings.Replace(catManifest, `"idle":"idle.svg"`, `"idle":"idle.svg","wave":"wave.svg"`, 1), map[string]string{"idle.svg": okSVG}, `"wave"`},
		{"outside", KindCharacter, catManifest, map[string]string{"idle.svg": `<svg><image href="https://x.invalid/a.png"/></svg>`}, "outside"},
		{"another name", KindCharacter, strings.Replace(catManifest, `"cat"`, `"dog"`, 1), map[string]string{"idle.svg": okSVG}, "name"},
		{"unknown field", KindCharacter, strings.Replace(catManifest, `"version":1`, `"version":1,"colour":"red"`, 1), map[string]string{"idle.svg": okSVG}, "manifest.json is not valid"},
		{"unused file", KindCharacter, catManifest, map[string]string{"idle.svg": okSVG, "extra.svg": okSVG}, `"extra.svg"`},
		{"bad file name", KindCharacter, catManifest, map[string]string{"../idle.svg": okSVG}, "file name"},
		{"background without its file", KindBackground, `{"name":"cat","description":"x","version":1,"file":"a.svg"}`, map[string]string{}, "not there"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := CheckLibraryItem(c.kind, "cat", []byte(c.manifest), c.files)
			if len(p) == 0 || !strings.Contains(strings.Join(p, " "), c.want) {
				t.Fatalf("%v, want %q", p, c.want)
			}
		})
	}
	if p := CheckLibraryItem(KindCharacter, "Bad Name", []byte(catManifest), nil); len(p) != 1 {
		t.Fatal(p)
	}
}

func TestTheLibraryFoldersAreImportedAndTheInvalidOnesLeftOut(t *testing.T) {
	dir := t.TempDir()
	writeItem(t, dir, "characters", "cat", catManifest, map[string]string{"idle.svg": okSVG})
	writeItem(t, dir, "characters", "ghost", `{"name":"ghost","description":"x","version":1,"width":10,"height":10,"anchor":{"x":0.5,"y":1},"poses":{"idle":"idle.svg","wave":"wave.svg"},"default_pose":"idle"}`, map[string]string{"idle.svg": okSVG})
	writeItem(t, dir, "backgrounds", "night", `{"name":"night","description":"x","version":1,"file":"night.svg"}`, map[string]string{"night.svg": okSVG})
	reg := store.NewRegistry()
	RegisterStore(reg, Folders{Library: dir})
	st, err := store.OpenMemory(reg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	recs, err := st.RunImports(context.Background(), "music-motion/library")
	if err != nil || recs[0].Created != 2 || len(recs[0].Skipped) != 1 || !strings.Contains(recs[0].Skipped[0].Reason, `"wave"`) {
		t.Fatalf("%+v %v", recs, err)
	}
	l := NewLibrary(DBLibrary(st))
	if c, ok := l.Character("cat"); !ok || c.Source != "user" || c.SVG["idle"] != okSVG {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := l.Character("ghost"); ok {
		t.Fatal("left out")
	}
	if _, ok := l.Background("night"); !ok {
		t.Fatal("the background came")
	}
	if _, err := os.Stat(dir + "/characters/cat/manifest.json"); err != nil {
		t.Fatal("files untouched")
	}
	if recs, _ := st.RunImports(context.Background(), "music-motion/library"); recs[0].Created != 0 {
		t.Fatalf("a second run creates nothing: %+v", recs)
	}
	if missing, _ := st.CheckSchema(); len(missing) > 0 {
		t.Fatal(missing)
	}
}
