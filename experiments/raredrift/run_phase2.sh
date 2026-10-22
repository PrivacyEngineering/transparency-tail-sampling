#!/usr/bin/env bash
# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
# Second part of the schedule actually run: (1) re-run of the tailrandom replicate-1 cells (the first attempt used a config
# with a YAML error and exported nothing; the fixed config is in configs/tailrandom.yaml), (2) replicate 2 of the cheap cells
# while time remains. Env: DEADLINE_EPOCH (cells only start if they can finish before it).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); C="$HERE/run_cell.sh"
export TAILRANDOM_PCT=15 DRAIN=38
cell() { [ $(( $(date +%s) + $4 + DRAIN + 25 )) -gt ${DEADLINE_EPOCH:-9999999999} ] && { echo "SKIP (deadline) $*"; return; }; "$C" "$1" "$2" "$3" "$4" "$5" 5000 "$DRAIN"; }
for g in "V1 0.1 60" "V1 0.01 60" "V3 0.01 60" "V1 0.001 90"; do set -- $g; cell tailrandom $1 $2 $3 1; done
for g in "V1 0.01 60" "V3 0.01 60" "V1 0.1 60"; do set -- $g; for cfg in head1 head10 tailrandom tailattr tatsbasic tats; do cell $cfg $1 $2 $3 2; done; done
docker compose -f "$HERE/docker-compose.raredrift.yml" down >/dev/null 2>&1
echo "ALL DONE $(date -u +%FT%TZ)"
