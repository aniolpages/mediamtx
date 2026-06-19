package core

import (
	"bytes"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	hi264encode "github.com/Eyevinn/hi264/pkg/encode"
	hi264yuv "github.com/Eyevinn/hi264/pkg/yuv"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/stretchr/testify/require"
)

func newFreeTCPAddress(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	return ln.Addr().String()
}

func testH264AccessUnit(t *testing.T) (*description.Media, *format.H264, [][]byte) {
	t.Helper()

	grid, err := hi264yuv.ParseGrid("x")
	require.NoError(t, err)

	enc := &hi264encode.FrameEncoder{
		Grid: grid,
		Colors: hi264yuv.ColorMap{
			'x': {Y: 128, Cb: 128, Cr: 128},
		},
		QP: 26,
	}

	annexB, err := enc.Encode()
	require.NoError(t, err)

	nalus, err := testAnnexBToNALUs(annexB)
	require.NoError(t, err)

	var sps []byte
	var pps []byte

	for _, nalu := range nalus {
		switch nalu[0] & 0x1F {
		case 7:
			sps = nalu

		case 8:
			pps = nalu
		}
	}

	require.NotNil(t, sps)
	require.NotNil(t, pps)

	h264Format := &format.H264{
		PayloadTyp:        96,
		SPS:               sps,
		PPS:               pps,
		PacketizationMode: 1,
	}
	media := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{h264Format},
	}

	return media, h264Format, nalus
}

func testAnnexBToNALUs(byts []byte) ([][]byte, error) {
	var out [][]byte
	next := 0

	for {
		start, startCodeLen := testFindAnnexBStartCode(byts, next)
		if start < 0 {
			break
		}

		naluStart := start + startCodeLen
		nextStart, _ := testFindAnnexBStartCode(byts, naluStart)
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

func testFindAnnexBStartCode(byts []byte, from int) (int, int) {
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

func TestCoreSnapshots(t *testing.T) {
	snapshotsAddress := newFreeTCPAddress(t)

	p, ok := newInstance(t,
		"rtmp: no\n"+
			"hls: no\n"+
			"webrtc: no\n"+
			"srt: no\n"+
			"moq: no\n"+
			"snapshots: yes\n"+
			"snapshotsAddress: "+snapshotsAddress+"\n"+
			"snapshotsTimeout: 2s\n"+
			"authInternalUsers:\n"+
			"  - user: any\n"+
			"    permissions:\n"+
			"      - action: publish\n"+
			"      - action: read\n"+
			"        path: allowed\n"+
			"paths:\n"+
			"  allowed:\n"+
			"  denied:\n")
	require.Equal(t, true, ok)
	defer p.Close()

	media, h264Format, au := testH264AccessUnit(t)

	source := gortsplib.Client{}
	err := source.StartRecording("rtsp://localhost:8554/allowed",
		&description.Session{Medias: []*description.Media{media}})
	require.NoError(t, err)
	defer source.Close()

	encoder, err := h264Format.CreateEncoder()
	require.NoError(t, err)

	terminateWriter := make(chan struct{})
	defer close(terminateWriter)

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		var timestamp uint32

		for {
			select {
			case <-ticker.C:
				pkts, err := encoder.Encode(au)
				if err != nil {
					continue
				}

				for _, pkt := range pkts {
					pkt.Timestamp = timestamp
					_ = source.WritePacketRTP(media, pkt)
				}

				timestamp += 90000

			case <-terminateWriter:
				return
			}
		}
	}()

	res, err := http.Get("http://" + snapshotsAddress + "/allowed/snapshot.png?width=8")
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "image/png", res.Header.Get("Content-Type"))

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	cfg, err := png.DecodeConfig(bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, 8, cfg.Width)
	require.Equal(t, 8, cfg.Height)

	res, err = http.Get("http://" + snapshotsAddress + "/denied/snapshot.png")
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	require.Equal(t, `Basic realm="mediamtx"`, res.Header.Get("WWW-Authenticate"))
}
