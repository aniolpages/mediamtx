package frameextractor

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testJPEG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := range 2 {
		for x := range 4 {
			img.SetRGBA(x, y, color.RGBA{R: uint8(30 + x*40), G: uint8(80 + y*60), B: 120, A: 255})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}

func TestSnapshotMJPEGPassthrough(t *testing.T) {
	in := testJPEG(t)

	out, contentType, err := Snapshot(CodecMJPEG, in, Options{})
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", contentType)
	require.Equal(t, in, out)

	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, 4, cfg.Width)
	require.Equal(t, 2, cfg.Height)
}

func TestSnapshotMJPEGResizePNG(t *testing.T) {
	out, contentType, err := Snapshot(CodecMJPEG, testJPEG(t), Options{
		Format: FormatPNG,
		Width:  2,
	})
	require.NoError(t, err)
	require.Equal(t, "image/png", contentType)

	cfg, err := png.DecodeConfig(bytes.NewReader(out))
	require.NoError(t, err)
	require.Equal(t, 2, cfg.Width)
	require.Equal(t, 1, cfg.Height)
}

func TestDecodeVP8(t *testing.T) {
	img, err := DecodeVP8(testVP8Payload(t))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 150, 100), img.Bounds())
}

func TestH264H265RandomAccessHelpers(t *testing.T) {
	require.False(t, H264AccessUnitIsRandomAccess(AccessUnit{{0x01}}))
	require.True(t, H264AccessUnitIsRandomAccess(AccessUnit{{0x67}, {0x68}, {0x65}}))

	require.False(t, H265AccessUnitIsRandomAccess(AccessUnit{{0x02, 0x01}}))
	require.True(t, H265AccessUnitIsRandomAccess(AccessUnit{{19 << 1, 0x01}}))
}

func TestExtractKeyFrame(t *testing.T) {
	au := AccessUnit{{0x67, 0x01}, {0x68, 0x02}, {0x65, 0x03}}

	kf, err := ExtractKeyFrame(CodecH264, au)
	require.NoError(t, err)
	require.Equal(t, au, kf)

	au[0][1] = 0xFF
	require.Equal(t, byte(0x01), kf[0][1])

	_, err = ExtractKeyFrame(CodecH264, AccessUnit{{0x41, 0x01}})
	require.ErrorIs(t, err, ErrNoKeyFrame)
}

func TestFirstKeyFrame(t *testing.T) {
	kf, err := FirstKeyFrame(CodecH265, []AccessUnit{
		{{0x02, 0x01}},
		{{19 << 1, 0x01}, {0x26, 0x02}},
	})
	require.NoError(t, err)
	require.Equal(t, AccessUnit{{19 << 1, 0x01}, {0x26, 0x02}}, kf)
}

func TestAnnexBToNALUs(t *testing.T) {
	nalus, err := AnnexBToNALUs([]byte{0, 0, 1, 0x65, 0x01, 0, 0, 0, 1, 0x41, 0x02})
	require.NoError(t, err)
	require.Equal(t, AccessUnit{{0x65, 0x01}, {0x41, 0x02}}, AccessUnit(nalus))
}

func TestNALUsToAnnexB(t *testing.T) {
	require.Equal(t,
		[]byte{0, 0, 0, 1, 0x65, 0x01, 0, 0, 0, 1, 0x41, 0x02},
		NALUsToAnnexB(AccessUnit{{0x65, 0x01}, {0x41, 0x02}}))
}

func TestUnsupportedH264Decode(t *testing.T) {
	_, err := DecodeH264AccessUnit(AccessUnit{{0x65}})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUnsupportedCodec))
}

func testVP8Payload(t *testing.T) []byte {
	t.Helper()

	byts, err := os.ReadFile(filepath.Join(xImageModuleDir(t), "testdata", "blue-purple-pink.lossy.webp"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(byts), 12)
	require.Equal(t, []byte("RIFF"), byts[:4])
	require.Equal(t, []byte("WEBP"), byts[8:12])

	pos := 12
	for pos+8 <= len(byts) {
		chunk := string(byts[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(byts[pos+4 : pos+8]))
		start := pos + 8
		end := start + size
		require.LessOrEqual(t, end, len(byts))

		if chunk == "VP8 " {
			return byts[start:end]
		}

		pos = end + (size & 1)
	}

	t.Fatal("VP8 chunk not found")
	return nil
}

func xImageModuleDir(t *testing.T) string {
	t.Helper()

	bi, ok := debug.ReadBuildInfo()
	require.True(t, ok)

	var version string
	for _, dep := range bi.Deps {
		if dep.Path == "golang.org/x/image" {
			version = dep.Version
			break
		}
	}
	if version == "" {
		version = xImageVersionFromGoMod(t)
	}
	require.NotEmpty(t, version)

	modCache := os.Getenv("GOMODCACHE")
	if modCache == "" {
		goPath := os.Getenv("GOPATH")
		if goPath == "" {
			home, err := os.UserHomeDir()
			require.NoError(t, err)
			goPath = filepath.Join(home, "go")
		}
		modCache = filepath.Join(goPath, "pkg", "mod")
	}

	return filepath.Join(modCache, "golang.org", "x", "image@"+version)
}

func xImageVersionFromGoMod(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		byts, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(byts), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[0] == "golang.org/x/image" {
					return fields[1]
				}
			}
			return ""
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
