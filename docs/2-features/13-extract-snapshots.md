# Extract snapshots

MediaMTX can serve snapshots from H.264 streams through the native snapshots
server:

```yml
snapshots: yes
snapshotsAddress: :9995
```

Then request a JPEG or PNG image from a path:

```sh
curl -o snapshot.jpg http://localhost:9995/mystream/snapshot.jpg
curl -o snapshot.png 'http://localhost:9995/mystream/snapshot.png?width=640&quality=85'
```

Available query parameters are:

- `width`: resize to this width while preserving aspect ratio when `height` is
  omitted.
- `height`: resize to this height while preserving aspect ratio when `width` is
  omitted.
- `fit`: resize mode when both dimensions are set. Available values are
  `contain`, `cover` and `stretch`.
- `quality`: JPEG quality, from 1 to 100.

Snapshots use the same `read` permissions as other readers, so path-restricted
users can only extract snapshots from paths they can read.

The native snapshots server currently supports H.264 IDR frames. If a stream has
no supported video codec, no decodable key frame arrives before
`snapshotsTimeout`, or frame decoding fails, the server returns a JSON error.

As an alternative, you can periodically extract snapshots from available streams
by using FFmpeg inside the `runOnReady` hook:

```yml
pathDefaults:
  runOnReady: |
    bash -c "
    while true; do
      mkdir -p $(dirname snapshots/$MTX_PATH)
      ffmpeg -i rtsp://localhost:8554/$MTX_PATH -frames:v 1 -update true -y snapshots/$MTX_PATH.jpg
      sleep 10
    done"
```

Where `10` is the interval between snapshots.
