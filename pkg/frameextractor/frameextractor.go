// Package frameextractor extracts compressed keyframes and decodes supported
// single-frame payloads into images.
//
// The package is intentionally independent from MediaMTX internals. Callers can
// pass codec payloads extracted from RTP, RTMP, HLS, or any other transport.
package frameextractor

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"

	"golang.org/x/image/draw"
	"golang.org/x/image/vp8"
)

// Codec is a compressed frame codec.
type Codec string

// Supported codec identifiers.
const (
	CodecMJPEG Codec = "mjpeg"
	CodecVP8   Codec = "vp8"
	CodecH264  Codec = "h264"
	CodecH265  Codec = "h265"
)

// ImageFormat is an output image format.
type ImageFormat string

// Supported output image formats.
const (
	FormatJPEG ImageFormat = "jpeg"
	FormatPNG  ImageFormat = "png"
)

// FitMode controls how a decoded image is resized when both Width and Height
// are set.
type FitMode string

// Supported fit modes.
const (
	// FitContain preserves aspect ratio and fits inside Width x Height.
	FitContain FitMode = "contain"

	// FitCover preserves aspect ratio and crops the center to Width x Height.
	FitCover FitMode = "cover"

	// FitStretch ignores aspect ratio.
	FitStretch FitMode = "stretch"
)

var (
	// ErrUnsupportedCodec is returned when the requested codec has no pure-Go
	// decoder in this package.
	ErrUnsupportedCodec = errors.New("unsupported codec")

	// ErrUnsupportedFrame is returned when a codec is supported but the provided
	// frame cannot be decoded in isolation.
	ErrUnsupportedFrame = errors.New("unsupported frame")

	// ErrNoVisibleFrame is returned when the input is valid but does not contain
	// a displayable frame.
	ErrNoVisibleFrame = errors.New("no visible frame")

	// ErrNoKeyFrame is returned when an access unit does not contain a keyframe.
	ErrNoKeyFrame = errors.New("no keyframe")
)

// AccessUnit is a compressed frame expressed as codec-specific NAL units.
// H.264 and H.265 access units use one NAL unit per slice/parameter-set item.
type AccessUnit [][]byte

// Options controls image transformation and encoding.
type Options struct {
	// Format selects the output image format. Empty means JPEG.
	Format ImageFormat

	// Quality controls JPEG quality, from 1 to 100. Zero means 85.
	Quality int

	// Width and Height optionally resize the image. When one dimension is zero,
	// the other one is calculated from the input aspect ratio.
	Width  int
	Height int

	// Fit controls resizing when both Width and Height are set. Empty means
	// FitContain.
	Fit FitMode
}

func (o Options) imageFormat() ImageFormat {
	if o.Format == "" {
		return FormatJPEG
	}
	return o.Format
}

func (o Options) quality() int {
	if o.Quality == 0 {
		return 85
	}
	return o.Quality
}

func (o Options) fit() FitMode {
	if o.Fit == "" {
		return FitContain
	}
	return o.Fit
}

func (o Options) validates() error {
	switch o.imageFormat() {
	case FormatJPEG, FormatPNG:
	default:
		return fmt.Errorf("invalid image format: %s", o.Format)
	}

	if o.Quality < 0 || o.Quality > 100 {
		return fmt.Errorf("invalid JPEG quality: %d", o.Quality)
	}

	if o.Width < 0 {
		return fmt.Errorf("invalid width: %d", o.Width)
	}

	if o.Height < 0 {
		return fmt.Errorf("invalid height: %d", o.Height)
	}

	switch o.fit() {
	case FitContain, FitCover, FitStretch:
	default:
		return fmt.Errorf("invalid fit mode: %s", o.Fit)
	}

	return nil
}

// Validate checks whether options are valid.
func (o Options) Validate() error {
	return o.validates()
}

func (o Options) canPassThroughJPEG() bool {
	return (o.Format == "" || o.Format == FormatJPEG) &&
		o.Quality == 0 &&
		o.Width == 0 &&
		o.Height == 0
}

// Snapshot decodes payload and encodes it according to opts.
func Snapshot(codec Codec, payload []byte, opts Options) ([]byte, string, error) {
	if err := opts.validates(); err != nil {
		return nil, "", err
	}

	if codec == CodecMJPEG && opts.canPassThroughJPEG() {
		_, err := jpeg.DecodeConfig(bytes.NewReader(payload))
		if err != nil {
			return nil, "", err
		}
		return append([]byte(nil), payload...), "image/jpeg", nil
	}

	img, err := Decode(codec, payload)
	if err != nil {
		return nil, "", err
	}

	return Encode(img, opts)
}

// Decode decodes a compressed single-frame payload into an image.
func Decode(codec Codec, payload []byte) (image.Image, error) {
	switch codec {
	case CodecMJPEG:
		return DecodeMJPEG(payload)

	case CodecVP8:
		return DecodeVP8(payload)

	case CodecH264:
		return DecodeH264AnnexB(payload)

	case CodecH265:
		return nil, fmt.Errorf("%w: H.265 pixel reconstruction is not implemented", ErrUnsupportedCodec)

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCodec, codec)
	}
}

// DecodeMJPEG decodes a complete JPEG frame.
func DecodeMJPEG(payload []byte) (image.Image, error) {
	return jpeg.Decode(bytes.NewReader(payload))
}

// DecodeVP8 decodes a raw VP8 frame payload.
//
// This decodes an intra/key frame independently. Inter frames require decoder
// state from previous frames and are rejected.
func DecodeVP8(payload []byte) (image.Image, error) {
	d := vp8.NewDecoder()
	d.Init(bytes.NewReader(payload), len(payload))

	fh, err := d.DecodeFrameHeader()
	if err != nil {
		return nil, err
	}

	if !fh.KeyFrame {
		return nil, fmt.Errorf("%w: VP8 inter frames need previous decoder state", ErrUnsupportedFrame)
	}

	if !fh.ShowFrame {
		return nil, ErrNoVisibleFrame
	}

	return d.DecodeFrame()
}

// DecodeH265AccessUnit documents the intended H.265 integration point.
func DecodeH265AccessUnit(_ [][]byte) (image.Image, error) {
	return nil, fmt.Errorf("%w: H.265 access unit parsing is available, pixel reconstruction is not implemented",
		ErrUnsupportedCodec)
}

// ExtractKeyFrame returns a copy of au when it is independently decodable.
//
// For H.264 this means an IDR access unit. For H.265 this means an IRAP access
// unit. The returned data is still compressed video; use DecodeH264AccessUnit
// or SnapshotH264AccessUnit to turn an H.264 keyframe into pixels.
func ExtractKeyFrame(codec Codec, au AccessUnit) (AccessUnit, error) {
	switch codec {
	case CodecH264:
		if !H264AccessUnitIsRandomAccess(au) {
			return nil, ErrNoKeyFrame
		}
		return cloneAccessUnit(au), nil

	case CodecH265:
		if !H265AccessUnitIsRandomAccess(au) {
			return nil, ErrNoKeyFrame
		}
		return cloneAccessUnit(au), nil

	default:
		return nil, fmt.Errorf("%w: keyframe extraction from access units is implemented for H.264 and H.265",
			ErrUnsupportedCodec)
	}
}

// FirstKeyFrame returns the first keyframe in accessUnits.
func FirstKeyFrame(codec Codec, accessUnits []AccessUnit) (AccessUnit, error) {
	for _, au := range accessUnits {
		kf, err := ExtractKeyFrame(codec, au)
		if err == nil {
			return kf, nil
		}

		if !errors.Is(err, ErrNoKeyFrame) {
			return nil, err
		}
	}

	return nil, ErrNoKeyFrame
}

func cloneAccessUnit(au AccessUnit) AccessUnit {
	out := make(AccessUnit, len(au))
	for i, nalu := range au {
		out[i] = append([]byte(nil), nalu...)
	}
	return out
}

// H264AccessUnitIsRandomAccess reports whether au contains an IDR NAL unit.
func H264AccessUnitIsRandomAccess(au [][]byte) bool {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}

		if nalu[0]&0x1F == 5 {
			return true
		}
	}

	return false
}

// H265AccessUnitIsRandomAccess reports whether au contains an IRAP NAL unit.
func H265AccessUnitIsRandomAccess(au [][]byte) bool {
	for _, nalu := range au {
		if len(nalu) < 2 {
			continue
		}

		typ := (nalu[0] >> 1) & 0x3F
		if typ >= 16 && typ <= 23 {
			return true
		}
	}

	return false
}

// AnnexBToNALUs splits an Annex-B byte stream into NAL units without start
// codes. It works for both H.264 and H.265 Annex-B payloads.
func AnnexBToNALUs(byts []byte) ([][]byte, error) {
	var out [][]byte
	next := 0

	for {
		start, startCodeLen := findAnnexBStartCode(byts, next)
		if start < 0 {
			break
		}

		naluStart := start + startCodeLen
		nextStart, _ := findAnnexBStartCode(byts, naluStart)
		if nextStart < 0 {
			nextStart = len(byts)
		}

		if naluStart < nextStart {
			out = append(out, byts[naluStart:nextStart])
		}

		next = nextStart
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("Annex-B payload contains no NAL units")
	}

	return out, nil
}

// NALUsToAnnexB serializes NAL units with four-byte Annex-B start codes.
func NALUsToAnnexB(nalus AccessUnit) []byte {
	var out []byte
	for _, nalu := range nalus {
		out = append(out, 0, 0, 0, 1)
		out = append(out, nalu...)
	}
	return out
}

func findAnnexBStartCode(byts []byte, from int) (int, int) {
	for i := from; i+3 <= len(byts); i++ {
		if byts[i] != 0 || byts[i+1] != 0 {
			continue
		}

		if byts[i+2] == 1 {
			return i, 3
		}

		if i+4 <= len(byts) && byts[i+2] == 0 && byts[i+3] == 1 {
			return i, 4
		}
	}

	return -1, 0
}

// Encode transforms img according to opts and encodes it.
func Encode(img image.Image, opts Options) ([]byte, string, error) {
	if err := opts.validates(); err != nil {
		return nil, "", err
	}

	img = Resize(img, opts)

	buf := bytes.NewBuffer(nil)

	switch opts.imageFormat() {
	case FormatJPEG:
		err := jpeg.Encode(buf, img, &jpeg.Options{Quality: opts.quality()})
		if err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/jpeg", nil

	case FormatPNG:
		err := png.Encode(buf, img)
		if err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil

	default:
		panic("validated above")
	}
}

// Resize applies the resize portion of opts and returns the original image when
// no resize is requested.
func Resize(img image.Image, opts Options) image.Image {
	src := img.Bounds()
	srcW := src.Dx()
	srcH := src.Dy()

	if srcW <= 0 || srcH <= 0 || (opts.Width == 0 && opts.Height == 0) {
		return img
	}

	dstW, dstH := targetSize(srcW, srcH, opts)
	if dstW == srcW && dstH == srcH {
		return img
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	if opts.fit() != FitCover {
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, src, draw.Over, nil)
		return dst
	}

	scale := math.Max(float64(dstW)/float64(srcW), float64(dstH)/float64(srcH))
	scaledW := max(1, int(math.Round(float64(srcW)*scale)))
	scaledH := max(1, int(math.Round(float64(srcH)*scale)))

	tmp := image.NewRGBA(image.Rect(0, 0, scaledW, scaledH))
	draw.CatmullRom.Scale(tmp, tmp.Bounds(), img, src, draw.Over, nil)

	offX := max(0, (scaledW-dstW)/2)
	offY := max(0, (scaledH-dstH)/2)
	draw.Draw(dst, dst.Bounds(), tmp, image.Point{X: offX, Y: offY}, draw.Src)

	return dst
}

func targetSize(srcW int, srcH int, opts Options) (int, int) {
	if opts.Width == 0 {
		return max(1, int(math.Round(float64(opts.Height)*float64(srcW)/float64(srcH)))), opts.Height
	}

	if opts.Height == 0 {
		return opts.Width, max(1, int(math.Round(float64(opts.Width)*float64(srcH)/float64(srcW))))
	}

	if opts.fit() == FitStretch || opts.fit() == FitCover {
		return opts.Width, opts.Height
	}

	scale := math.Min(float64(opts.Width)/float64(srcW), float64(opts.Height)/float64(srcH))
	return max(1, int(math.Round(float64(srcW)*scale))),
		max(1, int(math.Round(float64(srcH)*scale)))
}

// DecodeJPEGConfig validates a JPEG payload and returns its dimensions.
func DecodeJPEGConfig(payload []byte) (image.Config, error) {
	return jpeg.DecodeConfig(bytes.NewReader(payload))
}

// WriteSnapshot writes Snapshot output into w.
func WriteSnapshot(w io.Writer, codec Codec, payload []byte, opts Options) (string, error) {
	byts, contentType, err := Snapshot(codec, payload, opts)
	if err != nil {
		return "", err
	}

	_, err = w.Write(byts)
	if err != nil {
		return "", err
	}

	return contentType, nil
}
