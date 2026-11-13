# SlideBolt HDHomeRun Plugin

Discovers configured HDHomeRun tuners, exposes channels and one recording
switch per tuner, imports XMLTV guide data, and writes recordings as file
devices. Timers are provided by `sb-scheduler`.

## Configuration

- `HDHOMERUN_DEVICES`: comma-separated tuner base URLs (defaults to `http://hdhomerun.local`).
- `HDHOMERUN_XMLTV_URL`: optional XMLTV URL; otherwise the official DeviceAuth XMLTV endpoint is used.
- `HDHOMERUN_RECORDING_DIR`: container output directory (defaults to `/recordings`).
- `HDHOMERUN_RECORDING_PROFILE`: `universal-mp4` (default), `raw-ts`,
  `remux-mkv`, or `h264-mp4`. Universal recordings capture to a staging TS,
  then serialize H.264/AAC MP4 encoding and remove the source only after the
  result passes validation.
