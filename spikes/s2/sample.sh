#!/bin/sh
# Spike S2: sample CPU% and RSS of the spike server and every S2Spike app
# process (simulator apps are ordinary macOS processes) every 10 s.
# Usage: sample.sh <seconds> <out.csv>
dur=${1:-600}
out=${2:-sample.csv}
echo "t,pid,pcpu,rss_kb,cputime,command" > "$out"
end=$(( $(date +%s) + dur ))
while [ "$(date +%s)" -lt "$end" ]; do
  t=$(date +%s)
  ps -axo pid=,pcpu=,rss=,time=,comm= | grep -E 's2server|S2Spike' | grep -v grep |
    awk -v t="$t" '{n=split($5,a,"/"); print t "," $1 "," $2 "," $3 "," $4 "," a[n]}' >> "$out"
  sleep 10
done
