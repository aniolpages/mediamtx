# frameextractor

`frameextractor` is an isolated pure-Go experiment for extracting keyframes and
turning supported compressed video frames into still images.

It currently supports:

- M-JPEG: decodes complete JPEG frames and can return them as-is.
- VP8: decodes standalone key frames through `golang.org/x/image/vp8`.
- H.264/H.265: identifies and extracts compressed keyframe access units.
- JPEG and PNG output, optional resizing, and JPEG quality control.

It deliberately does not include FFmpeg, OpenCV, CGo, system libraries, or
generated native artifacts.

H.264 and H.265 pixel reconstruction is not implemented yet. Extracting a
keyframe access unit is much simpler than generating a JPEG/PNG from it:
MediaMTX already extracts H.264/H.265 access units, and this package can select
the independently decodable ones. Converting those compressed access units into
pixels still requires implementing the video decoder itself.

For H.264/H.265-only keyframe extraction, use `ExtractKeyFrame` or
`FirstKeyFrame` with access units already split into NAL units. Use
`NALUsToAnnexB` when the caller needs a byte stream with Annex-B start codes.
