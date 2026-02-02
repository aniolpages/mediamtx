package stream

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"os"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/av1"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h265"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	mcodecs "github.com/bluenviron/mediacommon/v2/pkg/formats/mp4/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/pmp4"
	"github.com/bluenviron/mediamtx/internal/unit"
)

//go:embed offline_av1.mp4
var offlineAV1 []byte

//go:embed offline_vp9.mp4
var offlineVP9 []byte

//go:embed offline_h265.mp4
var offlineH265 []byte

//go:embed offline_h264.mp4
var offlineH264 []byte

type offlineSubStreamTrack struct {
	wg             *sync.WaitGroup
	file           string
	pos            int
	ctx            context.Context
	subStream      *SubStream
	media          *description.Media
	format         format.Format
	waitLastSample bool
	startTime      time.Time // Shared start time for A/V sync
}

func (t *offlineSubStreamTrack) initialize() {
	t.wg.Add(1)
	go t.run()
}

func (t *offlineSubStreamTrack) run() {
	defer t.wg.Done()

	var pts int64

	if t.file != "" {
		f, err := os.Open(t.file)
		if err != nil {
			panic(err)
		}
		defer f.Close()

		err = t.runFile(pts, f, t.pos)
		if err != nil {
			panic(err)
		}
		return
	}

	const audioWritesPerSecond = 10

	// Use shared start time for embedded audio/video (for A/V sync)
	var elapsedTime time.Duration

	switch forma := t.format.(type) {
	case *format.Opus:
		unitsPerWrite := (forma.ClockRate() / 960) / audioWritesPerSecond
		writeDuration := 960 * int64(unitsPerWrite)
		writeDurationGo := multiplyAndDivide2(time.Duration(writeDuration), time.Second, 48000)

		for {
			payload := make(unit.PayloadOpus, unitsPerWrite)
			for i := range payload {
				payload[i] = []byte{0xF8, 0xFF, 0xFE} // DTX frame
			}

			t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
				PTS:     pts,
				NTP:     time.Time{},
				Payload: payload,
			})

			pts += writeDuration
			elapsedTime += writeDurationGo

			if !t.sleep(t.startTime.Add(elapsedTime)) {
				return
			}
		}

	case *format.MPEG4Audio:
		unitsPerWrite := (forma.ClockRate() / mpeg4audio.SamplesPerAccessUnit) / audioWritesPerSecond
		writeDuration := mpeg4audio.SamplesPerAccessUnit * int64(unitsPerWrite)
		writeDurationGo := multiplyAndDivide2(time.Duration(writeDuration), time.Second, time.Duration(forma.ClockRate()))

		for {
			var frame []byte
			switch forma.Config.ChannelConfig {
			case 1:
				frame = []byte{0x01, 0x18, 0x20, 0x07}

			default:
				frame = []byte{0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}
			}

			payload := make(unit.PayloadMPEG4Audio, unitsPerWrite)
			for i := range payload {
				payload[i] = frame
			}

			t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
				PTS:     pts,
				NTP:     time.Time{},
				Payload: payload,
			})

			pts += writeDuration
			elapsedTime += writeDurationGo

			if !t.sleep(t.startTime.Add(elapsedTime)) {
				return
			}
		}

	case *format.G711:
		samplesPerWrite := forma.ClockRate() / audioWritesPerSecond
		writeDuration := samplesPerWrite
		writeDurationGo := multiplyAndDivide2(time.Duration(writeDuration), time.Second, time.Duration(forma.ClockRate()))

		for {
			var sample byte
			if forma.MULaw {
				sample = 0xFF
			} else {
				sample = 0xD5
			}

			payload := make(unit.PayloadG711, samplesPerWrite*forma.ChannelCount)
			for i := range payload {
				payload[i] = sample
			}

			t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
				PTS:     pts,
				NTP:     time.Time{},
				Payload: payload,
			})

			pts += int64(writeDuration)
			elapsedTime += writeDurationGo

			if !t.sleep(t.startTime.Add(elapsedTime)) {
				return
			}
		}

	case *format.LPCM:
		samplesPerWrite := forma.ClockRate() / audioWritesPerSecond
		writeDuration := samplesPerWrite
		writeDurationGo := multiplyAndDivide2(time.Duration(writeDuration), time.Second, time.Duration(forma.ClockRate()))

		for {
			payload := make(unit.PayloadLPCM, samplesPerWrite*forma.ChannelCount*(forma.BitDepth/8))

			t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
				PTS:     pts,
				NTP:     time.Time{},
				Payload: payload,
			})

			pts += int64(writeDuration)
			elapsedTime += writeDurationGo

			if !t.sleep(t.startTime.Add(elapsedTime)) {
				return
			}
		}

	default:
		var buf []byte

		switch t.format.(type) {
		case *format.AV1:
			buf = offlineAV1

		case *format.VP9:
			buf = offlineVP9

		case *format.H265:
			buf = offlineH265

		case *format.H264:
			buf = offlineH264

		default:
			panic("should not happen")
		}

		r := bytes.NewReader(buf)

		err := t.runFile(pts, r, 0)
		if err != nil {
			panic(err)
		}
	}
}

func (t *offlineSubStreamTrack) runFile(pts int64, r io.ReadSeeker, pos int) error {
	var presentation pmp4.Presentation
	err := presentation.Unmarshal(r)
	if err != nil {
		return err
	}

	track := presentation.Tracks[pos]

	// Start DTS from 0. Both audio and video will start sending at the same time
	// because they share the same startTime.
	dts := pts

	// Use the shared start time for A/V synchronization
	var elapsedSamples int64 // Total sample time elapsed in track timescale units

	for {
		for _, sample := range track.Samples {
			var payload []byte
			payload, err = sample.GetPayload()
			if err != nil {
				return err
			}

			// Convert PTSOffset from track timescale to codec clock rate
			ptsOffsetInClockRate := multiplyAndDivide(int64(sample.PTSOffset),
				int64(t.format.ClockRate()), int64(track.TimeScale))

			// PTS = DTS + PTSOffset
			// For H.264/H.265 with B-frames, this preserves the proper relationship
			// between DTS and PTS that DTSExtractor expects. The first IDR will have
			// PTS = PTSOffset (not 0), which is correct for B-frame reordering.
			// Note: This may cause a small A/V desync (video appears ~66ms after audio
			// for typical 2-frame B-delay), but this is necessary for DTSExtractor
			// to compute valid DTS values.
			samplePTS := dts + ptsOffsetInClockRate

			switch track.Codec.(type) {
			case *mcodecs.AV1:
				var bs av1.Bitstream
				err = bs.Unmarshal(payload)
				if err != nil {
					return err
				}

				t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
					PTS:     samplePTS,
					NTP:     time.Time{},
					Payload: unit.PayloadAV1(bs),
				})

			case *mcodecs.VP9:
				t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
					PTS:     samplePTS,
					NTP:     time.Time{},
					Payload: unit.PayloadVP9(payload),
				})

			case *mcodecs.H265:
				var avcc h264.AVCC
				err = avcc.Unmarshal(payload)
				if err != nil {
					return err
				}

				// Prepend VPS/SPS/PPS to IDR frames to signal source change to downstream handlers
				// (e.g., when transitioning from online to offline in alwaysAvailable mode)
				h265Codec := track.Codec.(*mcodecs.H265)
				for _, nalu := range avcc {
					typ := h265.NALUType((nalu[0] >> 1) & 0b111111)
					if typ == h265.NALUType_IDR_W_RADL || typ == h265.NALUType_IDR_N_LP || typ == h265.NALUType_CRA_NUT {
						// Prepend VPS, SPS and PPS before the IDR
						avcc = append([][]byte{h265Codec.VPS, h265Codec.SPS, h265Codec.PPS}, avcc...)
						break
					}
				}

				t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
					PTS:     samplePTS,
					NTP:     time.Time{},
					Payload: unit.PayloadH265(avcc),
				})

			case *mcodecs.H264:
				var avcc h264.AVCC
				err = avcc.Unmarshal(payload)
				if err != nil {
					return err
				}

				// Prepend SPS/PPS to IDR frames to signal source change to downstream handlers
				// (e.g., when transitioning from online to offline in alwaysAvailable mode)
				h264Codec := track.Codec.(*mcodecs.H264)
				for _, nalu := range avcc {
					if h264.NALUType(nalu[0]&0x1F) == h264.NALUTypeIDR {
						// Prepend SPS and PPS before the IDR
						avcc = append([][]byte{h264Codec.SPS, h264Codec.PPS}, avcc...)
						break
					}
				}

				t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
					PTS:     samplePTS,
					NTP:     time.Time{},
					Payload: unit.PayloadH264(avcc),
				})

			case *mcodecs.MPEG4Audio:
				t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
					PTS:     samplePTS,
					NTP:     time.Time{},
					Payload: unit.PayloadMPEG4Audio([][]byte{payload}),
				})

			case *mcodecs.Opus:
				t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
					PTS:     samplePTS,
					NTP:     time.Time{},
					Payload: unit.PayloadOpus([][]byte{payload}),
				})

			case *mcodecs.LPCM:
				t.subStream.WriteUnit(t.media, t.format, &unit.Unit{
					PTS:     samplePTS,
					NTP:     time.Time{},
					Payload: unit.PayloadLPCM(payload),
				})
			}

			// DTS increments by sample duration (not affected by PTSOffset)
			dtsDelta := multiplyAndDivide(int64(sample.Duration),
				int64(t.format.ClockRate()), int64(track.TimeScale))
			dts += dtsDelta

			// Track elapsed time based on DTS (decode time)
			elapsedSamples += int64(sample.Duration)

			// Sleep based on DTS (decode time), not PTS
			// This ensures frames are sent in decode order at the right pace
			// The receiver will handle reordering based on PTS
			elapsedDuration := multiplyAndDivide2(time.Duration(elapsedSamples),
				time.Second, time.Duration(track.TimeScale))
			targetTime := t.startTime.Add(elapsedDuration)

			if !t.sleep(targetTime) {
				return nil
			}
		}
	}
}

func (t *offlineSubStreamTrack) sleep(systemTime time.Time) bool {
	select {
	case <-time.After(time.Until(systemTime)):
	case <-t.ctx.Done():
		if t.waitLastSample {
			time.Sleep(time.Until(systemTime))
		}
		return false
	}
	return true
}
