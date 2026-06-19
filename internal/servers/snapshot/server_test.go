package snapshot

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
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/auth"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/externalcmd"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/test"
	"github.com/bluenviron/mediamtx/internal/unit"
)

type dummyPath struct {
	name string
	conf *conf.Path
}

func (p *dummyPath) Name() string {
	return p.name
}

func (p *dummyPath) SafeConf() *conf.Path {
	return p.conf
}

func (p *dummyPath) ExternalCmdEnv() externalcmd.Environment {
	return externalcmd.Environment{}
}

func (p *dummyPath) RemovePublisher(_ defs.PathRemovePublisherReq) {
}

func (p *dummyPath) RemoveReader(req defs.PathRemoveReaderReq) {
}

func newTestAddress(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	return ln.Addr().String()
}

func newH264Stream(t *testing.T) (*stream.Stream, *stream.SubStream, *description.Media, *format.H264, unit.PayloadH264) {
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

	nalus, err := annexBToNALUs(annexB)
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
	strm := &stream.Stream{
		OrigDesc:          &description.Session{Medias: []*description.Media{media}},
		WriteQueueSize:    512,
		RTPMaxPayloadSize: 1450,
		Parent:            test.NilLogger,
	}
	require.NoError(t, strm.Initialize())

	subStream := &stream.SubStream{
		Stream:        strm,
		UseRTPPackets: false,
	}
	require.NoError(t, subStream.Initialize())

	return strm, subStream, media, h264Format, unit.PayloadH264(nalus)
}

func annexBToNALUs(byts []byte) ([][]byte, error) {
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

func TestServerSnapshotH264(t *testing.T) {
	strm, subStream, media, h264Format, payload := newH264Stream(t)

	pathConf := &conf.Path{}
	path := &dummyPath{name: "teststream", conf: pathConf}
	pm := &test.PathManager{
		AddReaderImpl: func(req defs.PathAddReaderReq) (*defs.PathAddReaderRes, error) {
			require.Equal(t, "teststream", req.AccessRequest.Name)
			require.Equal(t, "width=8", req.AccessRequest.Query)
			require.Equal(t, "myuser", req.AccessRequest.Credentials.User)
			require.Equal(t, "mypass", req.AccessRequest.Credentials.Pass)
			require.Equal(t, auth.ProtocolSnapshot, req.AccessRequest.Proto)
			require.NotNil(t, req.AccessRequest.ID)
			require.False(t, req.AccessRequest.Publish)

			return &defs.PathAddReaderRes{
				Path:   path,
				User:   "myuser",
				Stream: strm,
			}, nil
		},
	}

	addr := newTestAddress(t)
	s := &Server{
		Address:         addr,
		AllowOrigins:    []string{"*"},
		ReadTimeout:     conf.Duration(10 * time.Second),
		WriteTimeout:    conf.Duration(10 * time.Second),
		SnapshotTimeout: conf.Duration(2 * time.Second),
		PathManager:     pm,
		Parent:          test.NilLogger,
	}
	require.NoError(t, s.Initialize())
	defer s.Close()

	terminateWriter := make(chan struct{})
	defer close(terminateWriter)

	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				subStream.WriteUnit(media, h264Format, &unit.Unit{
					PTS:     0,
					Payload: payload,
				})

			case <-terminateWriter:
				return
			}
		}
	}()

	req, err := http.NewRequest(http.MethodGet, "http://myuser:mypass@"+addr+"/teststream/snapshot.png?width=8", nil)
	require.NoError(t, err)

	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "image/png", res.Header.Get("Content-Type"))
	require.Equal(t, "no-store", res.Header.Get("Cache-Control"))

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	cfg, err := png.DecodeConfig(bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, 8, cfg.Width)
	require.Equal(t, 8, cfg.Height)
}

func TestServerSnapshotAuthError(t *testing.T) {
	addr := newTestAddress(t)
	s := &Server{
		Address:         addr,
		ReadTimeout:     conf.Duration(10 * time.Second),
		WriteTimeout:    conf.Duration(10 * time.Second),
		SnapshotTimeout: conf.Duration(time.Second),
		PathManager: &test.PathManager{
			AddReaderImpl: func(_ defs.PathAddReaderReq) (*defs.PathAddReaderRes, error) {
				return nil, &auth.Error{
					AskCredentials: true,
					Wrapped:        fmt.Errorf("auth error"),
				}
			},
		},
		Parent: test.NilLogger,
	}
	require.NoError(t, s.Initialize())
	defer s.Close()

	res, err := http.Get("http://" + addr + "/teststream/snapshot.jpg")
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusUnauthorized, res.StatusCode)
	require.Equal(t, `Basic realm="mediamtx"`, res.Header.Get("WWW-Authenticate"))
}

func TestServerSnapshotNoSupportedCodecs(t *testing.T) {
	mjpegFormat := &format.MJPEG{}
	media := &description.Media{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{mjpegFormat},
	}
	strm := &stream.Stream{
		OrigDesc:          &description.Session{Medias: []*description.Media{media}},
		WriteQueueSize:    512,
		RTPMaxPayloadSize: 1450,
		Parent:            test.NilLogger,
	}
	require.NoError(t, strm.Initialize())

	addr := newTestAddress(t)
	s := &Server{
		Address:         addr,
		ReadTimeout:     conf.Duration(10 * time.Second),
		WriteTimeout:    conf.Duration(10 * time.Second),
		SnapshotTimeout: conf.Duration(time.Second),
		PathManager: &test.PathManager{
			AddReaderImpl: func(_ defs.PathAddReaderReq) (*defs.PathAddReaderRes, error) {
				return &defs.PathAddReaderRes{
					Path:   &dummyPath{name: "teststream", conf: &conf.Path{}},
					Stream: strm,
				}, nil
			},
		},
		Parent: test.NilLogger,
	}
	require.NoError(t, s.Initialize())
	defer s.Close()

	res, err := http.Get("http://" + addr + "/teststream/snapshot.jpg")
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusUnsupportedMediaType, res.StatusCode)
}

func TestServerSnapshotTimeout(t *testing.T) {
	strm, _, _, _, _ := newH264Stream(t)

	addr := newTestAddress(t)
	s := &Server{
		Address:         addr,
		ReadTimeout:     conf.Duration(10 * time.Second),
		WriteTimeout:    conf.Duration(10 * time.Second),
		SnapshotTimeout: conf.Duration(100 * time.Millisecond),
		PathManager: &test.PathManager{
			AddReaderImpl: func(_ defs.PathAddReaderReq) (*defs.PathAddReaderRes, error) {
				return &defs.PathAddReaderRes{
					Path:   &dummyPath{name: "teststream", conf: &conf.Path{}},
					Stream: strm,
				}, nil
			},
		},
		Parent: test.NilLogger,
	}
	require.NoError(t, s.Initialize())
	defer s.Close()

	res, err := http.Get("http://" + addr + "/teststream/snapshot.jpg")
	require.NoError(t, err)
	defer res.Body.Close()

	require.Equal(t, http.StatusGatewayTimeout, res.StatusCode)
}
