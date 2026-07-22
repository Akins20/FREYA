package vision

import (
	"bytes"
	"image"
	"image/jpeg"
)

// decodeJPEG is a test helper for asserting on prepared output.
func decodeJPEG(data []byte) (image.Image, error) {
	return jpeg.Decode(bytes.NewReader(data))
}
