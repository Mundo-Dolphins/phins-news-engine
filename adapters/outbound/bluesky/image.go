package bluesky

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"

	"github.com/Mundo-Dolphins/phins-news-engine/domain/publishing"
	appout "github.com/Mundo-Dolphins/phins-news-engine/ports/out"
)

const (
	// maxBlobBytes is Bluesky's image upload limit (1,000,000 bytes).
	maxBlobBytes = 1_000_000
	// maxSourceBytes bounds what is read from the media store.
	maxSourceBytes = 32 << 20
	// maxSourcePixels bounds what is decoded for re-encoding.
	maxSourcePixels = 40_000_000
	// minShrinkSide stops downscaling: below this the image is unusable.
	minShrinkSide = 256
	maxImages     = 4
	maxAltRunes   = 2000
)

var allowedImageMIME = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// preparedImage is an image ready for uploadBlob.
type preparedImage struct {
	data          []byte
	mime          string
	alt           string
	width, height int // zero when unknown (webp)
}

// loadImage reads ref from the media store rooted at root, validates it and,
// when it exceeds Bluesky's size limit, re-encodes it as a smaller JPEG.
// Every failure is a PermanentError: a retry reads the same file.
func loadImage(root string, ref publishing.ImageRef) (preparedImage, error) {
	perm := func(format string, args ...any) error {
		return &appout.PermanentError{Err: fmt.Errorf("bluesky: image %q: "+format, append([]any{ref.Path}, args...)...)}
	}
	if !allowedImageMIME[ref.MIME] {
		return preparedImage{}, perm("unsupported media type %q (want image/jpeg, image/png or image/webp)", ref.MIME)
	}
	if countGraphemes(ref.Alt) > maxAltRunes {
		return preparedImage{}, perm("alt text exceeds %d characters", maxAltRunes)
	}
	if root == "" {
		return preparedImage{}, perm("no media root configured")
	}
	data, err := readMedia(root, ref.Path)
	if err != nil {
		return preparedImage{}, perm("%v", err)
	}
	if sniffed := http.DetectContentType(data); sniffed != ref.MIME {
		return preparedImage{}, perm("content is %s, payload says %s", sniffed, ref.MIME)
	}

	img := preparedImage{data: data, mime: ref.MIME, alt: ref.Alt}
	if ref.MIME != "image/webp" {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return preparedImage{}, perm("cannot decode: %v", err)
		}
		img.width, img.height = cfg.Width, cfg.Height
	}
	if len(data) <= maxBlobBytes {
		return img, nil
	}
	if ref.MIME == "image/webp" {
		return preparedImage{}, perm("%d bytes exceeds the %d byte limit and webp cannot be re-encoded", len(data), maxBlobBytes)
	}
	small, w, h, err := shrinkToJPEG(data, maxBlobBytes)
	if err != nil {
		return preparedImage{}, perm("%v", err)
	}
	return preparedImage{data: small, mime: "image/jpeg", alt: ref.Alt, width: w, height: h}, nil
}

// readMedia reads a store-relative path without letting it escape root
// (symlinks included), capped at maxSourceBytes.
func readMedia(root, rel string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open media root: %w", err)
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if len(data) > maxSourceBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxSourceBytes)
	}
	return data, nil
}

// shrinkToJPEG decodes a JPEG or PNG and re-encodes it as a JPEG of at most
// limit bytes, lowering the quality first and then the dimensions. Transparent
// pixels are flattened onto white.
func shrinkToJPEG(data []byte, limit int) (out []byte, width, height int, err error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("cannot decode: %w", err)
	}
	if cfg.Width*cfg.Height > maxSourcePixels {
		return nil, 0, 0, fmt.Errorf("%dx%d pixels is too large to re-encode", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("cannot decode: %w", err)
	}
	cur := flatten(src)
	for {
		for _, q := range []int{85, 72, 60} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, cur, &jpeg.Options{Quality: q}); err != nil {
				return nil, 0, 0, fmt.Errorf("re-encode: %w", err)
			}
			if buf.Len() <= limit {
				b := cur.Bounds()
				return buf.Bytes(), b.Dx(), b.Dy(), nil
			}
		}
		b := cur.Bounds()
		w, h := b.Dx()*3/4, b.Dy()*3/4
		if w < minShrinkSide || h < minShrinkSide {
			return nil, 0, 0, fmt.Errorf("cannot be brought under %d bytes", limit)
		}
		cur = resize(cur, w, h)
	}
}

// flatten draws src onto an opaque white RGBA canvas.
func flatten(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	return dst
}

// resize scales src to w x h by averaging the source pixels each target
// pixel covers (a box filter), which is enough for photo-sized shrinking.
func resize(src *image.RGBA, w, h int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, n uint32
			for sy := y0; sy < y1; sy++ {
				row := src.Pix[sy*src.Stride:]
				for sx := x0; sx < x1; sx++ {
					r += uint32(row[sx*4])
					g += uint32(row[sx*4+1])
					bl += uint32(row[sx*4+2])
					n++
				}
			}
			o := y*dst.Stride + x*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return dst
}

var _ = png.Decode // register the PNG decoder for image.Decode
