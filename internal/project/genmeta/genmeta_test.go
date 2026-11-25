package genmeta

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
	"unicode/utf16"

	"veritmo/internal/platform/runner"
)

// The exact text stable-diffusion.cpp wrote into a PNG and into a clip's
// `comment` tag on 2026-10-11 (the clip's one with its SDCPP JSON tail).
const sdPNGText = "photo of a rocky coastline at sunset, waves crashing, stormy clouds, detailed\n" +
	"Steps: 25, CFG scale: 7.000000, Guidance: 3.500000, Eta: inf, Seed: 42, Size: 512x512, Model: v1-5-pruned-emaonly.safetensors, RNG: cuda, Sampler: euler_a discrete, Version: stable-diffusion.cpp"

const sdClipComment = "photo of a rocky coastline at sunset, waves crashing, stormy clouds, wind\n" +
	"Steps: 25, CFG scale: 7.000000, Guidance: 3.500000, Eta: inf, Seed: 42, Size: 512x512, Model: v1-5-pruned-emaonly.safetensors, RNG: cuda, Sampler: euler karras, Version: stable-diffusion.cpp, " +
	`SDCPP: {"clip_skip":-1,"height":512,"mode":"vid_gen","models":{"model":"v1-5-pruned-emaonly.safetensors"},` +
	`"prompt":{"negative":"blurry, lowres","positive":"photo of a rocky coastline at sunset, waves crashing, stormy clouds, wind"},` +
	`"sampling":{"guidance":{"distilled_guidance":3.5,"txt_cfg":7.0},"method":"euler","scheduler":"karras","steps":25},` +
	`"high_noise_sampling":{"steps":20},"seed":42,"strength":0.75,"video":{"fps":8,"frame_count":16},"width":512}`

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	return b.Bytes()
}

func pngWith(chunks ...[]byte) []byte {
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, pngChunk("IHDR", make([]byte, 13))...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	out = append(out, pngChunk("IDAT", []byte{1, 2, 3})...)
	return append(out, pngChunk("IEND", nil)...)
}

func tEXt(key, text string) []byte { return pngChunk("tEXt", []byte(key+"\x00"+text)) }

func deflate(s string) []byte {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func TestSDPNGText(t *testing.T) {
	m := FromBytes(pngWith(tEXt("parameters", sdPNGText)))
	if m.Prompt != "photo of a rocky coastline at sunset, waves crashing, stormy clouds, detailed" {
		t.Errorf("prompt = %q", m.Prompt)
	}
	if m.Seed == nil || *m.Seed != 42 || m.Steps == nil || *m.Steps != 25 || m.CfgScale == nil || *m.CfgScale != 7 {
		t.Errorf("numbers = %+v", m)
	}
	if m.Model != "v1-5-pruned-emaonly.safetensors" || m.Sampler != "euler_a discrete" || *m.Width != 512 || *m.Height != 512 {
		t.Errorf("model/sampler/size = %+v", m)
	}
}

func TestNegativePromptAndCommasInValues(t *testing.T) {
	text := "a red apple, on a table\nsecond line\nNegative prompt: blurry\nSteps: 20, Sampler: DPM++ 2M, CFG scale: 7, Seed: 5, Size: 512x768, Model: my, model v2"
	m := FromText(text)
	if m.Prompt != "a red apple, on a table\nsecond line" || m.NegativePrompt != "blurry" {
		t.Errorf("prompts = %q / %q", m.Prompt, m.NegativePrompt)
	}
	if m.Model != "my, model v2" || m.Sampler != "DPM++ 2M" || *m.Seed != 5 {
		t.Errorf("params = %+v", m)
	}
}

func TestClipCommentUsesTheSDCPPTail(t *testing.T) {
	m := FromVideoTags(map[string]string{"comment": sdClipComment, "encoder": "Lavf"})
	if m.NegativePrompt != "blurry, lowres" || !strings.HasSuffix(m.Prompt, "wind") {
		t.Errorf("prompts = %q / %q", m.Prompt, m.NegativePrompt)
	}
	if *m.Seed != 42 || *m.Steps != 25 || *m.CfgScale != 7 || m.Model != "v1-5-pruned-emaonly.safetensors" {
		t.Errorf("= %+v", m)
	}
	if got := FromVideoTags(map[string]string{"COMMENT": sdClipComment}); got.Empty() {
		t.Error("tag names are matched case-insensitively")
	}
}

func TestOutOfRangeNumbersAreLeftOut(t *testing.T) {
	m := FromText("p\nSteps: 5000, CFG scale: 500, Seed: 99999999999999999999, Size: 99999x1")
	if m.Steps != nil || m.CfgScale != nil || m.Seed != nil || m.Width != nil || m.Height != nil {
		t.Errorf("numbers out of range must be left out: %+v", m)
	}
	if m.Prompt != "p" {
		t.Errorf("the rest is kept: %+v", m)
	}
	if m := FromText("p\nSteps: 20"); m.Steps == nil || *m.Steps != 20 {
		t.Errorf("in range is kept: %+v", m)
	}
}

func TestPromptIsClipped(t *testing.T) {
	m := FromText(strings.Repeat("é", 30000) + "\nSteps: 20")
	if n := len([]rune(m.Prompt)); n != MaxPromptRunes {
		t.Errorf("prompt has %d runes", n)
	}
}

func TestPlainTextWithoutAStepsLineIsNothing(t *testing.T) {
	if m := FromText("Created with GIMP"); !m.Empty() {
		t.Errorf("= %+v", m)
	}
}

func TestPNGChunkKinds(t *testing.T) {
	iTXtPlain := pngChunk("iTXt", append([]byte("parameters\x00\x00\x00\x00\x00"), []byte(sdPNGText)...))
	iTXtZ := pngChunk("iTXt", append([]byte("parameters\x00\x01\x00\x00\x00"), deflate(sdPNGText)...))
	zTXt := pngChunk("zTXt", append([]byte("parameters\x00\x00"), deflate(sdPNGText)...))
	for name, c := range map[string][]byte{"iTXt": iTXtPlain, "iTXt compressed": iTXtZ, "zTXt": zTXt} {
		if m := FromBytes(pngWith(c)); m.Seed == nil || *m.Seed != 42 {
			t.Errorf("%s: %+v", name, m)
		}
	}
}

func TestComfyUIPNG(t *testing.T) {
	graph := `{"3":{"class_type":"KSampler","inputs":{"seed":1234567890123456789,"steps":30,"cfg":6.5,"sampler_name":"euler",
	"model":["4",0],"positive":["6",0],"negative":["7",0],"latent_image":["5",0]}},
	"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"sd15.safetensors"}},
	"6":{"class_type":"CLIPTextEncode","inputs":{"text":"a castle on a hill","clip":["4",1]}},
	"7":{"class_type":"CLIPTextEncode","inputs":{"text":"ugly","clip":["4",1]}}}`
	m := FromBytes(pngWith(tEXt("prompt", graph), tEXt("workflow", "{}")))
	if m.Prompt != "a castle on a hill" || m.NegativePrompt != "ugly" || m.Model != "sd15.safetensors" {
		t.Errorf("= %+v", m)
	}
	if *m.Seed != 1234567890123456789 || *m.Steps != 30 || *m.CfgScale != 6.5 || m.Sampler != "euler" {
		t.Errorf("sampler = %+v", m)
	}
	// the wrapper some tools use
	if m := FromText(`{"prompt":` + graph + `}`); m.Prompt != "a castle on a hill" {
		t.Errorf("wrapped graph: %+v", m)
	}
	// a graph without a sampler says nothing
	if m := FromText(`{"1":{"class_type":"LoadImage","inputs":{}}}`); !m.Empty() {
		t.Errorf("= %+v", m)
	}
}

func TestMalformedAndHostilePNGs(t *testing.T) {
	good := pngWith(tEXt("parameters", sdPNGText))
	for name, data := range map[string][]byte{
		"truncated in a chunk": good[:len(good)-30],
		"lying length":         append([]byte("\x89PNG\r\n\x1a\n"), 0xFF, 0xFF, 0xFF, 0xF0, 't', 'E', 'X', 't'),
		"only the signature":   []byte("\x89PNG\r\n\x1a\n"),
		"empty":                nil,
		"random":               bytes.Repeat([]byte{0xAB, 0xCD}, 100),
	} {
		_ = FromBytes(data) // must not panic or hang
		if name == "truncated in a chunk" {
			// the chunk before the cut was complete: kept
			if m := FromBytes(data); m.Seed == nil {
				t.Errorf("%s: what was valid before the cut is kept: %+v", name, m)
			}
		}
	}
	// a decompression bomb is refused, the other chunks still count
	bomb := pngChunk("zTXt", append([]byte("parameters\x00\x00"), deflate(strings.Repeat("A", MaxDecompressed+10))...))
	if m := FromBytes(pngWith(bomb)); !m.Empty() {
		t.Errorf("bomb: %+v", m)
	}
	// an oversized text chunk is skipped without error
	big := tEXt("parameters", strings.Repeat("x", MaxValue*3)+"\nSteps: 20")
	if m := FromBytes(pngWith(big)); !m.Empty() {
		t.Errorf("oversized: %+v", m)
	}
	// a graph with too many nodes
	var sb strings.Builder
	sb.WriteString("{")
	for i := 0; i < MaxGraphNodes+5; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"` + strings.Repeat("1", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)) + `":{"class_type":"X","inputs":{}}`)
	}
	sb.WriteString("}")
	if m := FromComfyPrompt([]byte(sb.String())); !m.Empty() {
		t.Errorf("huge graph: %+v", m)
	}
}

func gifWith(comment string) []byte {
	g := []byte("GIF89a")
	g = append(g, 1, 0, 1, 0, 0x80, 0, 0) // 1x1, global color table of 2 entries
	g = append(g, 0, 0, 0, 255, 255, 255)
	g = append(g, 0x21, 0xFF, 11)
	g = append(g, []byte("NETSCAPE2.0")...)
	g = append(g, 3, 1, 0, 0, 0)
	if comment != "" {
		g = append(g, 0x21, 0xFE)
		for b := []byte(comment); len(b) > 0; {
			n := min(len(b), 255)
			g = append(g, byte(n))
			g = append(g, b[:n]...)
			b = b[n:]
		}
		g = append(g, 0)
	}
	g = append(g, 0x2C, 0, 0, 0, 0, 1, 0, 1, 0, 0, 2, 2, 0x44, 0x01, 0)
	return append(g, 0x3B)
}

func TestGIFComment(t *testing.T) {
	if m := FromBytes(gifWith(sdClipComment)); m.Seed == nil || *m.Seed != 42 || m.NegativePrompt != "blurry, lowres" {
		t.Errorf("= %+v", m)
	}
	if m := FromBytes(gifWith("Created with GIMP")); !m.Empty() {
		t.Errorf("an ordinary comment is not a generation text: %+v", m)
	}
	if m := FromBytes(gifWith("")); !m.Empty() {
		t.Errorf("= %+v", m)
	}
	if m := FromBytes(gifWith(sdClipComment)[:40]); !m.Empty() {
		t.Errorf("truncated: %+v", m)
	}
}

func riff(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := []byte("RIFF")
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	return append(out, body...)
}

func webpChunk(fourcc string, data []byte) []byte {
	c := []byte(fourcc)
	c = binary.LittleEndian.AppendUint32(c, uint32(len(data)))
	c = append(c, data...)
	if len(data)%2 == 1 {
		c = append(c, 0)
	}
	return c
}

// exifWithUserComment builds a big-endian TIFF with an Exif IFD holding a
// UNICODE UserComment, as A1111 writes.
func exifWithUserComment(text string) []byte {
	u := utf16.Encode([]rune(text))
	val := []byte("UNICODE\x00")
	for _, c := range u {
		val = binary.BigEndian.AppendUint16(val, c)
	}
	b := []byte("MM\x00\x2a")
	b = binary.BigEndian.AppendUint32(b, 8)
	// IFD0 at 8: one entry (ExifIFD pointer) -> 8 + 2 + 12 + 4 = 26
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint16(b, 0x8769)
	b = binary.BigEndian.AppendUint16(b, 4)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = binary.BigEndian.AppendUint32(b, 26)
	b = binary.BigEndian.AppendUint32(b, 0)
	// Exif IFD at 26: one entry (UserComment, UNDEFINED) with the value at 44
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint16(b, 0x9286)
	b = binary.BigEndian.AppendUint16(b, 7)
	b = binary.BigEndian.AppendUint32(b, uint32(len(val)))
	b = binary.BigEndian.AppendUint32(b, 44)
	b = binary.BigEndian.AppendUint32(b, 0)
	return append(b, val...)
}

func TestWebPExifAndXMP(t *testing.T) {
	m := FromBytes(riff(webpChunk("VP8 ", []byte{1, 2, 3}), webpChunk("EXIF", exifWithUserComment(sdPNGText))))
	if m.Seed == nil || *m.Seed != 42 || !strings.HasPrefix(m.Prompt, "photo of a rocky") {
		t.Errorf("EXIF: %+v", m)
	}
	xmp := `<x:xmpmeta><rdf:RDF><rdf:Description><dc:description><rdf:Alt><rdf:li xml:lang="x-default">` +
		strings.ReplaceAll(sdPNGText, "\n", "&#10;") + `</rdf:li></rdf:Alt></dc:description></rdf:Description></rdf:RDF></x:xmpmeta>`
	m = FromBytes(riff(webpChunk("XMP ", []byte(xmp))))
	if m.Seed == nil || *m.Seed != 42 {
		t.Errorf("XMP: %+v", m)
	}
	if m := FromBytes(riff(webpChunk("VP8 ", []byte{1, 2, 3}))); !m.Empty() {
		t.Errorf("no metadata: %+v", m)
	}
	if m := FromBytes(riff(webpChunk("EXIF", []byte("MM\x00\x2a\xff\xff\xff\xff")))); !m.Empty() {
		t.Errorf("garbage EXIF: %+v", m)
	}
}

func TestUnknownContentYieldsNothing(t *testing.T) {
	for _, data := range [][]byte{[]byte("\xff\xd8\xff\xe0 jpeg"), []byte("hello"), nil} {
		if m := FromBytes(data); !m.Empty() {
			t.Errorf("= %+v", m)
		}
	}
}

func TestVideoTagsThroughFFprobe(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetHandler(func(inv runner.Invocation) runner.ScriptedResult {
		if inv.Name != "ffprobe" {
			t.Errorf("ran %s", inv.Name)
		}
		doc := `{"format":{"tags":{"comment":` + jsonString(sdClipComment) + `}}}`
		return runner.ScriptedResult{Stdout: []byte(doc)}
	})
	if m := FromVideo(context.Background(), fr, "x.mp4"); m.Seed == nil || *m.Seed != 42 {
		t.Errorf("= %+v", m)
	}
	fr.SetHandler(func(runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Err: errors.New("no ffprobe")}
	})
	if m := FromVideo(context.Background(), fr, "x.mp4"); !m.Empty() {
		t.Errorf("ffprobe failing yields nothing: %+v", m)
	}
	fr.SetHandler(func(runner.Invocation) runner.ScriptedResult {
		return runner.ScriptedResult{Stdout: []byte(`{"format":{}}`)}
	})
	if m := FromVideo(context.Background(), fr, "x.mp4"); !m.Empty() {
		t.Errorf("no tags: %+v", m)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
