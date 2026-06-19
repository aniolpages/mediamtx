package frameextractor

import (
	"fmt"
	"image"

	hi264decoder "github.com/Eyevinn/hi264/pkg/decoder"
	hi264frame "github.com/Eyevinn/hi264/pkg/frame"
	hi264yuv "github.com/Eyevinn/hi264/pkg/yuv"
)

// H264Decoder decodes H.264 IDR access units into images.
//
// It is intentionally shaped like the small decoder types used by the
// bluenviron media libraries: configure fields, call Init, then call Decode.
type H264Decoder struct {
	// DisableDeblocking skips the in-loop deblocking filter. This is faster but
	// produces lower-quality images.
	DisableDeblocking bool

	decoder *hi264decoder.Decoder
}

// Init initializes the decoder.
func (d *H264Decoder) Init() error {
	d.decoder = hi264decoder.New()
	d.decoder.SkipDeblock = d.DisableDeblocking
	return nil
}

// Decode decodes an H.264 access unit split into NAL units.
//
// The access unit must contain an IDR NAL unit and the decoder must have seen
// the matching SPS/PPS, either in this access unit or in a previous Decode call.
func (d *H264Decoder) Decode(au AccessUnit) (image.Image, error) {
	if d.decoder == nil {
		err := d.Init()
		if err != nil {
			return nil, err
		}
	}

	hasIDR := H264AccessUnitIsRandomAccess(au)

	f, err := d.decoder.DecodeNALUs(au)
	if err != nil {
		if !hasIDR && err.Error() == "no IDR NALU found" {
			return nil, ErrNoKeyFrame
		}

		return nil, fmt.Errorf("decode H.264 access unit: %w", err)
	}

	return h264FrameToImage(f), nil
}

// Snapshot decodes an H.264 access unit and encodes it according to opts.
func (d *H264Decoder) Snapshot(au AccessUnit, opts Options) ([]byte, string, error) {
	img, err := d.Decode(au)
	if err != nil {
		return nil, "", err
	}

	return Encode(img, opts)
}

// DecodeH264AccessUnit decodes an H.264 access unit split into NAL units.
func DecodeH264AccessUnit(au AccessUnit) (image.Image, error) {
	var d H264Decoder

	err := d.Init()
	if err != nil {
		return nil, err
	}

	return d.Decode(au)
}

// DecodeH264AnnexB decodes the first H.264 IDR frame from an Annex-B byte
// stream.
func DecodeH264AnnexB(payload []byte) (image.Image, error) {
	nalus, err := AnnexBToNALUs(payload)
	if err != nil {
		return nil, err
	}

	return DecodeH264AccessUnit(nalus)
}

// SnapshotH264AccessUnit decodes an H.264 access unit and encodes it according
// to opts.
func SnapshotH264AccessUnit(au AccessUnit, opts Options) ([]byte, string, error) {
	var d H264Decoder

	err := d.Init()
	if err != nil {
		return nil, "", err
	}

	return d.Snapshot(au, opts)
}

func h264FrameToImage(f *hi264frame.Frame) image.Image {
	cs := hi264yuv.BT601
	if f.ColorDescriptionValid {
		cs = hi264yuv.ColorSpaceFromMatrixCoefficients(f.MatrixCoefficients)
	}

	rng := hi264yuv.LimitedRange
	if f.VideoFullRangeFlag {
		rng = hi264yuv.FullRange
	}

	return hi264yuv.FrameToImageCS(f, cs, rng)
}
