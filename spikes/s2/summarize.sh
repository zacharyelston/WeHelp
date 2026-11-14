#!/bin/sh
# Spike S2: summarise a sample.sh CSV: per process, average CPU% (from the
# cumulative CPU time over the window), mean and max RSS.
# Usage: summarize.sh sample.csv
awk -F, 'NR > 1 {
  key = $6 " pid " $2
  split($5, p, ":"); secs = (length(p) == 3) ? p[1]*3600 + p[2]*60 + p[3] : p[1]*60 + p[2]
  if (!(key in t0)) { t0[key] = $1; c0[key] = secs }
  t1[key] = $1; c1[key] = secs
  rss[key] += $4; n[key]++
  if ($4 > mx[key]) mx[key] = $4
}
END {
  printf "%-28s %8s %9s %9s %6s\n", "process", "cpu_pct", "rss_mb", "max_mb", "secs"
  for (k in n) {
    dt = t1[k] - t0[k]
    printf "%-28s %8.1f %9.1f %9.1f %6d\n", k, (dt > 0 ? 100 * (c1[k] - c0[k]) / dt : 0), rss[k] / n[k] / 1024, mx[k] / 1024, dt
  }
}' "$1"
