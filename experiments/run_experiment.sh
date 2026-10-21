#!/usr/bin/env bash
# One repetition of the sampling experiment. Usage (from the repository root):
#   experiments/run_experiment.sh RUN_ID [DURATION_S=300] [USERS=100] [SPAWN_RATE=10]
# Requires: the shop running with OTEL_COLLECTOR_HOST=fanout (see experiments/README.md), python3.
set -euo pipefail
RUN_ID=$1; DURATION=${2:-300}; USERS=${3:-100}; SPAWN=${4:-10}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT="$ROOT/experiments/raw/$RUN_ID"; mkdir -p "$OUT/sink"
export OTEL_COLLECTOR_HOST=fanout SINK_DIR="../experiments/raw/$RUN_ID/sink" TAILPROB_PCT=${TAILPROB_PCT:-10} DRIFT_REGEX=${DRIFT_REGEX:-^[01]}
DC="docker compose --project-directory $ROOT/deploy -f $ROOT/deploy/docker-compose.yml -f $ROOT/experiments/docker-compose.experiment.yml"
SAMPLERS="c-none c-head1 c-head10 c-tailprob c-tailpd c-tailthesis c-transpthesis c-transp c-transpstrict"
NAMES=(none head1 head10 tailprob tailpd tailthesis transpthesis transp transpstrict)

echo "[$RUN_ID] (re)starting fan-out, sampler and sink collectors (TAILPROB_PCT=$TAILPROB_PCT, DRIFT_REGEX=$DRIFT_REGEX)"
$DC --profile experiment rm -sf fanout $SAMPLERS sink >/dev/null 2>&1 || true
$DC --profile experiment up -d sink $SAMPLERS fanout
sleep 20
python3 "$ROOT/experiments/docker_stats.py" "$OUT/docker_stats.csv" "$RUN_ID" 5 & STATS=$!
date -u +%s > "$OUT/load_start_unix"
echo "[$RUN_ID] locust: $USERS users, spawn $SPAWN/s, ${DURATION}s"
$DC --profile load run --rm -T -v "$OUT:/out" --entrypoint locust loadgenerator \
  --headless -u "$USERS" -r "$SPAWN" -t "${DURATION}s" --csv /out/locust --only-summary > "$OUT/locust.log" 2>&1 || echo "locust exit $?"
date -u +%s > "$OUT/load_end_unix"
echo "[$RUN_ID] waiting 45 s for tail-sampling decisions (decision_wait 30 s) and export"
sleep 45
for i in "${!NAMES[@]}"; do curl -s --max-time 10 "localhost:$((18881+i))/metrics" > "$OUT/metrics_${NAMES[$i]}.prom"; done
curl -s --max-time 10 localhost:18880/metrics > "$OUT/metrics_fanout.prom"
kill $STATS
$DC --profile experiment stop -t 30 fanout
$DC --profile experiment stop -t 30 $SAMPLERS
sleep 5
$DC --profile experiment stop -t 30 sink
$DC --profile experiment logs --no-color fanout $SAMPLERS sink > "$OUT/collector_logs.txt" 2>&1 || true
echo "[$RUN_ID] done -> $OUT"
