// Package vision prepares images for a multimodal model to look at.
//
// # Why images are resized before sending
//
// The same lesson as audio: upload dominates latency. A full-resolution
// screenshot from this machine is a 1.5MB PNG, and sending it raw took 14
// seconds end to end. Downscaled to 1024px on the long edge and re-encoded as
// JPEG it is around 120KB, with no loss of anything a model can act on —
// nobody reads a 1080p screenshot at native resolution to answer "what does
// this error say".
//
// Scaling is done with a box filter in pure Go. `golang.org/x/image/draw` has
// better resamplers, but it is a dependency, and for downscaling a screenshot
// the difference is invisible to a model.
package vision

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	_ "image/jpeg" // registers the JPEG decoder
	_ "image/png"  // registers the PNG decoder
	"os"
	"path/filepath"
	"strings"
)

// maxDimension is the long-edge size images are reduced to.
//
// 1024 is the point past which models stop gaining accuracy on ordinary
// screenshots and documents, while the byte count keeps climbing.
const maxDimension = 1024

// jpegQuality trades a little fidelity for a lot of upload time.
const jpegQuality = 85

// maxSourceBytes refuses absurd inputs before decoding them into memory.
const maxSourceBytes = 64 << 20

// Image is a picture prepared for a model.
type Image struct {
	Data     []byte
	MimeType string
	// Width and Height are the dimensions actually sent.
	Width, Height int
	// OriginalWidth and OriginalHeight are what was on disk.
	OriginalWidth, OriginalHeight int
	// Note explains anything the caller should know, such as heavy downscaling.
	Note string
}

// Supported reports whether a path looks like an image this package can read.
func Supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// Prepare loads an image and reduces it to something worth uploading.
func Prepare(path string) (*Image, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("vision: %w", err)
	}
	if info.Size() > maxSourceBytes {
		return nil, fmt.Errorf("vision: %s is %d MB, too large to process",
			filepath.Base(path), info.Size()>>20)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("vision: open: %w", err)
	}
	defer f.Close()

	src, format, err := image.Decode(f)
	if err != nil {
		// WEBP and HEIC have no stdlib decoder. Say so precisely rather than
		// reporting a generic decode failure.
		return nil, fmt.Errorf("vision: cannot decode %s (%s) — "+
			"PNG, JPEG, GIF and BMP are supported; convert other formats first",
			filepath.Base(path), strings.TrimPrefix(filepath.Ext(path), "."))
	}

	bounds := src.Bounds()
	ow, oh := bounds.Dx(), bounds.Dy()
	if ow == 0 || oh == 0 {
		return nil, fmt.Errorf("vision: %s has zero dimensions", filepath.Base(path))
	}

	out := &Image{
		MimeType:       "image/jpeg",
		OriginalWidth:  ow,
		OriginalHeight: oh,
	}

	scaled := src
	if ow > maxDimension || oh > maxDimension {
		nw, nh := fit(ow, oh, maxDimension)
		scaled = downscale(src, nw, nh)
		out.Note = fmt.Sprintf("resized from %dx%d to %dx%d for upload", ow, oh, nw, nh)
	}

	b := scaled.Bounds()
	out.Width, out.Height = b.Dx(), b.Dy()

	// JPEG has no alpha; compositing onto white avoids the black background
	// that a naive conversion gives transparent PNGs — which is most
	// screenshots with rounded window corners.
	flat := image.NewRGBA(b)
	draw.Draw(flat, b, &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(flat, b, scaled, b.Min, draw.Over)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("vision: encode: %w", err)
	}
	out.Data = buf.Bytes()

	// Re-encoding is a means, not an end. Flat or synthetic images compress
	// better as lossless PNG than as JPEG, and converting them makes the upload
	// *larger* — the opposite of the point. When the source needed no resize
	// and is already smaller, send it untouched.
	if scaled == src && int64(len(out.Data)) >= info.Size() && isDirectlySendable(format) {
		original, err := os.ReadFile(path)
		if err == nil {
			out.Data = original
			out.MimeType = "image/" + format
			out.Note = "sent unchanged; re-encoding would have been larger"
		}
	}
	return out, nil
}

// isDirectlySendable reports whether a decoded format can be uploaded as-is.
// GIF is excluded: an animated one would send every frame.
func isDirectlySendable(format string) bool {
	return format == "png" || format == "jpeg"
}

// fit computes dimensions preserving aspect ratio within a maximum.
func fit(w, h, max int) (int, int) {
	if w >= h {
		return max, int(float64(h) * float64(max) / float64(w))
	}
	return int(float64(w) * float64(max) / float64(h)), max
}

// downscale reduces an image using a box filter: each destination pixel is the
// mean of the source pixels it covers.
//
// Nearest-neighbour would be faster and would alias text badly, which matters
// precisely because the common case is reading words off a screenshot.
func downscale(src image.Image, nw, nh int) image.Image {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))

	xRatio := float64(sw) / float64(nw)
	yRatio := float64(sh) / float64(nh)

	for y := range nh {
		y0 := int(float64(y) * yRatio)
		y1 := int(float64(y+1) * yRatio)
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := range nw {
			x0 := int(float64(x) * xRatio)
			x1 := int(float64(x+1) * xRatio)
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var r, g, b, a, n uint64
			for sy := y0; sy < y1 && sy < sh; sy++ {
				for sx := x0; sx < x1 && sx < sw; sx++ {
					pr, pg, pb, pa := src.At(sb.Min.X+sx, sb.Min.Y+sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					b += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			if n == 0 {
				continue
			}
			dst.Set(x, y, color.RGBA64{
				R: uint16(r / n), G: uint16(g / n),
				B: uint16(b / n), A: uint16(a / n),
			})
		}
	}
	return dst
}

// Describe summarises what was prepared.
func (i *Image) Describe() string {
	s := fmt.Sprintf("%dx%d JPEG, %d KB", i.Width, i.Height, len(i.Data)/1024)
	if i.Note != "" {
		s += " (" + i.Note + ")"
	}
	return s
}
