# frameextractor

`frameextractor` is an isolated pure-Go experiment for extracting keyframes and
turning supported compressed video frames into still images.

It currently supports:

- M-JPEG: decodes complete JPEG frames and can return them as-is.
- VP8: decodes standalone key frames through `golang.org/x/image/vp8`.
- H.264: decodes IDR access units through the pure-Go `github.com/Eyevinn/hi264`
  decoder.
- H.265: identifies and extracts compressed keyframe access units.
- JPEG and PNG output, optional resizing, and JPEG quality control.

It deliberately does not include FFmpeg, OpenCV, CGo, system libraries, or
generated native artifacts.

H.264 decoding is currently limited to 8-bit 4:2:0 IDR frames supported by
`hi264`. It is enough to prove the snapshot path for many common streaming
inputs, but it is not a full replacement for FFmpeg/libavcodec yet.

For H.264 snapshots from MediaMTX-style access units, use
`SnapshotH264AccessUnit`. For raw Annex-B data, use `Snapshot` with `CodecH264`.
For H.265-only keyframe extraction, use `ExtractKeyFrame` or `FirstKeyFrame`.
Use `NALUsToAnnexB` when the caller needs a byte stream with Annex-B start
codes.
