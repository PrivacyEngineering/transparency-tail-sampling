#!/usr/bin/env bash
# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
# One rare-drift cell: one collector config alone, one drift class, one drift rate p, one repetition.
# Usage: run_cell.sh CONFIG CLASS P DURATION_S REP [LOAD=5000] [DRAIN_S=40]
#   CONFIG in head1 head10 tailrandom tailattr tatsbasic tats ; CLASS in V1 V3 ; P e.g. 0.0001
# Env: RAW (default experiments/raw/raredrift), TAILRANDOM_PCT (default 20). Cells with done.txt are skipped (resume).
# Raw output per cell: generator.json, groundtruth.tsv (injected trace ids), retained.tsv (sink log), docker_stats.csv,
# metrics_end.prom, collector.log. The seed depends on (CLASS, P, REP) only, so all configs see the same traces.
set -euo pipefail
CFG=$1; CLS=$2; P=$3; DUR=$4; REP=$5; LOAD=${6:-5000}; DRAIN=${7:-40}
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
RAW=${RAW:-$ROOT/experiments/raw/raredrift}
OUT="$RAW/$CFG/$CLS/p$P/rep$REP"
[ -f "$OUT/done.txt" ] && exit 0
rm -rf "$OUT"; mkdir -p "$OUT"
export RD_CONFIG=$CFG RD_OUT=$OUT TAILRANDOM_PCT=${TAILRANDOM_PCT:-20}
DC="docker compose -f $HERE/docker-compose.raredrift.yml"
PEXP=$(python3 -c "import math,sys; print(int(round(-math.log10($P))))")
SEED=$((REP*1000 + PEXP*10 + $( [ "$CLS" = V1 ] && echo 1 || echo 3 )))
echo "$(date -u +%FT%TZ) cfg=$CFG class=$CLS p=$P dur=${DUR}s rep=$REP load=$LOAD seed=$SEED"
$DC rm -sf collector rdsink >/dev/null 2>&1 || true
$DC up -d --force-recreate collector rdsink >/dev/null 2>&1
sleep 6
CID=$($DC ps -q collector)
python3 "$ROOT/experiments/benchmark/stats_stream.py" "$CID" "$OUT/docker_stats.csv" "$CFG/$CLS/$P/rep$REP" & STATS=$!
$DC --profile gen run --rm -T generator gen -endpoint collector:4317 -rate "$LOAD" -duration "${DUR}s" -workers 4 -batch-traces 20 \
    -drift "$P" -class "$CLS" -seed "$SEED" -gt /data/groundtruth.tsv -out /data/generator.json > "$OUT/generator.stdout" 2> "$OUT/generator.err"
curl -s --max-time 5 localhost:19889/metrics > "$OUT/metrics_gen_end.prom" || true
sleep "$DRAIN"   # let every trace pass decision_wait (30 s) and reach the sink
curl -s --max-time 5 localhost:19889/metrics > "$OUT/metrics_end.prom" || true
kill $STATS 2>/dev/null || true
$DC logs --no-color collector > "$OUT/collector.log" 2>&1 || true
$DC stop -t 15 collector rdsink >/dev/null 2>&1
date +%s > "$OUT/done.txt"
