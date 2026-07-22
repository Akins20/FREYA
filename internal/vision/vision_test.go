package vision

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Pseudo-random noise, because a smooth gradient compresses to almost
	// nothing as PNG and is nothing like a real screenshot or photograph.
	seed := uint32(12345)
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				uint8(seed >> 24), uint8(seed >> 16), uint8(seed >> 8), 255,
			})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDownscalesLargeImages(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.png")
	writePNG(t, p, 1920, 1080)

	before, _ := os.Stat(p)
	img, err := Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d KB PNG -> %s", before.Size()/1024, img.Describe())

	if img.Width > maxDimension || img.Height > maxDimension {
		t.Errorf("not downscaled: %dx%d", img.Width, img.Height)
	}
	// Aspect ratio must survive, or charts and screenshots come out distorted.
	origRatio := 1920.0 / 1080.0
	newRatio := float64(img.Width) / float64(img.Height)
	if diff := origRatio - newRatio; diff > 0.02 || diff < -0.02 {
		t.Errorf("aspect ratio changed: %.3f -> %.3f", origRatio, newRatio)
	}
	// The whole point is a smaller upload.
	if int64(len(img.Data)) >= before.Size() {
		t.Errorf("prepared image (%d B) is not smaller than the source (%d B)",
			len(img.Data), before.Size())
	}
	if img.Note == "" {
		t.Error("resizing was not reported")
	}
}

func TestPrepareLeavesSmallImagesAlone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "small.png")
	writePNG(t, p, 400, 300)

	img, err := Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 400 || img.Height != 300 {
		t.Errorf("small image was resized to %dx%d", img.Width, img.Height)
	}
	if img.Note != "" {
		t.Errorf("reported a resize that did not happen: %q", img.Note)
	}
}

func TestTransparentPNGGetsWhiteBackground(t *testing.T) {
	// JPEG has no alpha. Without compositing, transparent regions turn black,
	// which is most screenshots with rounded window corners.
	//
	// The image must be large enough to require resizing, since a small one
	// takes the passthrough path and keeps its PNG alpha untouched — correct
	// behaviour, but not what this test is checking.
	p := filepath.Join(t.TempDir(), "alpha.png")
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	for y := range 1200 {
		for x := range 1600 {
			img.Set(x, y, color.RGBA{0, 0, 0, 0}) // fully transparent
		}
	}
	f, _ := os.Create(p)
	png.Encode(f, img)
	f.Close()

	prepared, err := Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Data) == 0 {
		t.Fatal("no output")
	}
	// A fully transparent source composited onto white should encode small and
	// uniform; a black result would be equally small, so decode and check.
	decoded, err := decodeJPEG(prepared.Data)
	if err != nil {
		t.Fatal(err)
	}
	bounds := decoded.Bounds()
	r, g, b, _ := decoded.At(bounds.Dx()/2, bounds.Dy()/2).RGBA()
	if r < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Errorf("transparent pixel became dark (%d,%d,%d) — alpha was not composited",
			r>>8, g>>8, b>>8)
	}
}

func TestSupportedFormats(t *testing.T) {
	for _, p := range []string{"a.png", "b.JPG", "c.jpeg", "d.gif"} {
		if !Supported(p) {
			t.Errorf("%s reported unsupported", p)
		}
	}
	for _, p := range []string{"a.txt", "b.pdf", "c.docx"} {
		if Supported(p) {
			t.Errorf("%s reported supported", p)
		}
	}
}

func TestUndecodableFileGivesAUsefulError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fake.png")
	if err := os.WriteFile(p, []byte("this is not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(p)
	if err == nil {
		t.Fatal("garbage was accepted as an image")
	}
	// The message must say what to do, not merely that decoding failed.
	if !contains(err.Error(), "supported") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := Prepare("/nonexistent/x.png"); err == nil {
		t.Fatal("missing file did not error")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestNeverEnlargesTheUpload covers the case that broke first: an image that
// compresses better losslessly must not be re-encoded into something larger.
func TestNeverEnlargesTheUpload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "flat.png")
	// A uniform image: tiny as PNG, comparatively bulky as JPEG.
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := range 600 {
		for x := range 800 {
			img.Set(x, y, color.RGBA{200, 200, 200, 255})
		}
	}
	f, _ := os.Create(p)
	png.Encode(f, img)
	f.Close()

	before, _ := os.Stat(p)
	prepared, err := Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source %d B -> prepared %d B (%s)", before.Size(), len(prepared.Data), prepared.Note)

	if int64(len(prepared.Data)) > before.Size() {
		t.Errorf("upload grew from %d to %d bytes", before.Size(), len(prepared.Data))
	}
}
