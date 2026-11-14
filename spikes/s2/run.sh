#!/bin/sh
# Spike S2: start a measured run between two simulators.
# Usage: run.sh <rooms, e.g. c1 or c2a,c2b> <seconds> <out.csv>
# The spike server must already be running (see README.md).
# EXTRA="-keepResolution YES" passes more launch arguments to the app.
set -e
rooms=$1
dur=${2:-600}
out=${3:-sample.csv}
IPAD=${IPAD:-A1BE5968-7D49-46A7-A0E6-C525B613BC19}
IPHONE=${IPHONE:-3DBCE466-3E1E-4742-8DBC-8D1F93AD1422}
BUNDLE=com.zacelston.wehelp.spike2
here=$(cd "$(dirname "$0")" && pwd)
xcrun simctl launch --terminate-running-process "$IPAD" $BUNDLE -rooms "$rooms" -autojoin YES $EXTRA
sleep 3
xcrun simctl launch --terminate-running-process "$IPHONE" $BUNDLE -rooms "$rooms" -autojoin YES $EXTRA
sleep 15
"$here/sample.sh" "$dur" "$out"
