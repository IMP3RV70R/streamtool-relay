#!/bin/sh
set -eu
fixture_dir=$(mktemp -d /tmp/streamtool-fallback.XXXXXX)
trap 'rm -rf "$fixture_dir"' EXIT HUP INT TERM
# The non-root fixture container must write its temporary bind mount on Linux.
chmod 0777 "$fixture_dir"
# A moving patch on the default image lets the decoded-output probe verify both
# fallback identity and actual motion over multiple loop iterations.
docker compose -f compose.control.yml run --rm --no-deps \
  --volume "$PWD/backend/assets/offline.png:/tmp/reference.png:ro" \
  --volume "$fixture_dir:/output" publisher -q \
  compositor name=c '!' video/x-raw,format=I420,width=1280,height=720,framerate=30/1 \
  '!' videoconvert '!' x264enc threads=2 tune=zerolatency speed-preset=veryfast bitrate=3000 key-int-max=60 \
  '!' h264parse '!' mp4mux '!' filesink location=/output/loop.mp4 \
  filesrc location=/tmp/reference.png '!' pngdec '!' imagefreeze num-buffers=90 \
  '!' videoconvert '!' videoscale '!' videorate \
  '!' video/x-raw,format=I420,width=1280,height=720,framerate=30/1 '!' c.sink_0 \
  videotestsrc pattern=ball num-buffers=90 '!' video/x-raw,width=160,height=90,framerate=30/1 '!' c.sink_1
CONTROL_TEST_MEDIA_ONLY=1 CONTROL_TEST_WIDTH=1920 CONTROL_TEST_HEIGHT=1080 \
CONTROL_TEST_FPS=60 CONTROL_TEST_VIDEO_KBPS=6000 CONTROL_TEST_BFRAMES=2 \
CONTROL_TEST_FALLBACK_FILE="$fixture_dir/loop.mp4" make control-e2e
