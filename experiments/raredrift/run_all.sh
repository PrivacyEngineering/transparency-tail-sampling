#!/usr/bin/env bash
# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
# Rare-drift sweep driver (the schedule actually run; see experiments/README.md). Cells run strictly one after another.
# Env: DEADLINE_MIN (default 125) wall-clock cap: a cell is only started if it can finish before the deadline.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); C="$HERE/run_cell.sh"
export TAILRANDOM_PCT=${TAILRANDOM_PCT:-15}   # = TATS retained span share at 5000 spans/s (pilot: 15.2 %)
export DRAIN=${DRAIN:-38}
CFGS="head1 head10 tailrandom tailattr tatsbasic tats"
START=$(date +%s); DEADLINE=$((START + ${DEADLINE_MIN:-125}*60))
cell() { # cfg class p dur rep
  [ $(( $(date +%s) + $4 + DRAIN + 25 )) -gt $DEADLINE ] && { echo "SKIP (deadline) $*"; return; }
  "$C" "$1" "$2" "$3" "$4" "$5" 5000 "$DRAIN"
}
# Phase A: replicate 1 of the main grid; groups ordered by cost so that a cut leaves complete groups
for g in "V1 0.1 60" "V1 0.01 60" "V3 0.01 60" "V1 0.001 90" "V1 0.0001 300"; do
  set -- $g; for cfg in $CFGS; do cell $cfg $1 $2 $3 1; done
done
# Phase B: V3 at 1e-4 for the two TILT-aware configs only
for cfg in tatsbasic tats; do cell $cfg V3 0.0001 300 1; done
# Phase C: extra replicates of the cheap cells while time remains
for rep in 2 3; do
  for g in "V1 0.1 60" "V1 0.01 60" "V3 0.01 60"; do
    set -- $g; for cfg in $CFGS; do cell $cfg $1 $2 $3 $rep; done
  done
done
docker compose -f "$HERE/docker-compose.raredrift.yml" down >/dev/null 2>&1
echo "ALL DONE $(date -u +%FT%TZ)"
