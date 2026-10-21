#!/usr/bin/env bash
# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
# One repetition of experiment v2. Usage (from the repository root):
#   experiments/v2/run_v2.sh RUN_ID [DURATION_S=300] [USERS=100] [SPAWN_RATE=10]
# Env: TAILPROB_PCT (default 30), EXPERIMENT_TILT (default ../tilt/tilt.corrected.json, relative to deploy/),
#      TILT_SHA256 (sha256 of that file), DRIFT_V1/V2/V3 (trace-id regexes; "^$" disables a class).
set -euo pipefail
RUN_ID=$1; DURATION=${2:-300}; USERS=${3:-100}; SPAWN=${4:-10}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT="$ROOT/experiments/raw/v2/$RUN_ID"; mkdir -p "$OUT/sink"
export OTEL_COLLECTOR_HOST=fanout SINK_DIR="../experiments/raw/v2/$RUN_ID/sink" TAILPROB_PCT=${TAILPROB_PCT:-30}
DC="docker compose --project-directory $ROOT/deploy -f $ROOT/deploy/docker-compose.yml -f $ROOT/experiments/v2/docker-compose.v2.yml"
NAMES=(none head1 head10 tailprob tailthesis transpthesis transp hardened hardenedcomp)
SAMPLERS=$(printf "c2-%s " "${NAMES[@]}")
env | grep -E "^(TAILPROB_PCT|EXPERIMENT_TILT|TILT_SHA256|DRIFT_V)" | sort > "$OUT/env.txt" || true
echo "[$RUN_ID] (re)starting fan-out, samplers, sink"
$DC --profile exp2 rm -sf fanout $SAMPLERS sink >/dev/null 2>&1 || true
$DC --profile exp2 up -d sink $SAMPLERS fanout
sleep 20
python3 "$ROOT/experiments/docker_stats.py" "$OUT/docker_stats.csv" "$RUN_ID" 5 & STATS=$!
date -u +%s > "$OUT/load_start_unix"
echo "[$RUN_ID] locust: $USERS users, spawn $SPAWN/s, ${DURATION}s"
$DC --profile load run --rm -T -v "$OUT:/out" --entrypoint locust loadgenerator \
  --headless -u "$USERS" -r "$SPAWN" -t "${DURATION}s" --csv /out/locust --only-summary > "$OUT/locust.log" 2>&1 || echo "locust exit $?"
date -u +%s > "$OUT/load_end_unix"
sleep 45
for i in "${!NAMES[@]}"; do curl -s --max-time 10 "localhost:$((18881+i))/metrics" > "$OUT/metrics_${NAMES[$i]}.prom"; done
curl -s --max-time 10 localhost:18880/metrics > "$OUT/metrics_fanout.prom"
kill $STATS
$DC --profile exp2 stop -t 30 fanout
$DC --profile exp2 stop -t 30 $SAMPLERS
sleep 5
$DC --profile exp2 stop -t 30 sink
$DC --profile exp2 logs --no-color fanout $SAMPLERS sink > "$OUT/collector_logs.txt" 2>&1 || true
echo "[$RUN_ID] done -> $OUT"
