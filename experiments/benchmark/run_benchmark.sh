#!/usr/bin/env bash
# Collector performance benchmark (run on an otherwise idle Docker host: stop the shop first).
# Usage: experiments/benchmark/run_benchmark.sh REPS "CONFIGS" "LOADS" [WARMUP_S=20] [STEADY_S=120]
#   e.g. experiments/benchmark/run_benchmark.sh 3 "B0-none B1-tailsampling-thesis" "500 1000" 20 120
# Env BENCH_SET (default "sweep") names the series. Raw output: experiments/raw/benchmark/$BENCH_SET/<config>/<load>/rep<k>/
# (cells that already have done.txt are skipped, so an interrupted series can be resumed).
set -euo pipefail
REPS=$1; CONFIGS=$2; LOADS=$3; WARMUP=${4:-20}; STEADY=${5:-120}
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
DC="docker compose -f $HERE/docker-compose.benchmark.yml"
for rep in $(seq 1 "$REPS"); do
 for load in $LOADS; do
  for cfg in $CONFIGS; do
   OUT="$ROOT/experiments/raw/benchmark/${BENCH_SET:-sweep}/$cfg/$load/rep$rep"
   [ -f "$OUT/done.txt" ] && continue
   mkdir -p "$OUT"
   echo "$(date -u +%FT%TZ) rep=$rep load=$load cfg=$cfg"
   export BENCH_CONFIG=$cfg
   $DC rm -sf collector >/dev/null 2>&1 || true
   $DC up -d --force-recreate collector >/dev/null 2>&1
   sleep 5
   CID=$($DC ps -q collector)
   python3 "$HERE/stats_stream.py" "$CID" "$OUT/docker_stats.csv" "$cfg/$load/rep$rep" & STATS=$!
   T0=$(date -u +%s)
   $DC --profile gen run --rm -T generator -endpoint collector:4317 -rate "$load" -duration "$((WARMUP+STEADY))s" \
       -workers 4 -batch-traces 20 -drift 0.10 -seed "$rep" > "$OUT/generator.json" 2> "$OUT/generator.err" &
   GEN=$!
   sleep "$WARMUP"; curl -s --max-time 5 localhost:19888/metrics > "$OUT/metrics_start.prom"; python3 -c 'import time; print(time.time())' > "$OUT/steady_start"
   sleep "$STEADY"; curl -s --max-time 5 localhost:19888/metrics > "$OUT/metrics_end.prom"; python3 -c 'import time; print(time.time())' > "$OUT/steady_end"
   wait $GEN || true
   kill $STATS || true
   $DC logs --no-color collector > "$OUT/collector.log" 2>&1 || true
   $DC stop -t 10 collector >/dev/null 2>&1
   echo "$T0" > "$OUT/done.txt"
  done
 done
done
$DC down >/dev/null 2>&1 || true
