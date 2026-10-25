#!/usr/bin/env bash
# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
# Phase 3: remaining repetitions 2 and 3 of the rare-drift sweep (rarest cells first).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); C="$HERE/run_cell.sh"
export TAILRANDOM_PCT=15 DRAIN=38
CFGS="head1 head10 tailrandom tailattr tatsbasic tats"
cell() { "$C" "$1" "$2" "$3" "$4" "$5" 5000 "$DRAIN"; }
# rep 2 (cells not yet measured in phase 1/2)
for cfg in $CFGS; do cell $cfg V1 0.0001 300 2; done
for cfg in tatsbasic tats; do cell $cfg V3 0.0001 300 2; done
for cfg in $CFGS; do cell $cfg V1 0.001 90 2; done
for cfg in $CFGS; do cell $cfg V1 0.1 60 2; done
for cfg in tatsbasic tats; do cell $cfg V3 0.01 60 2; done
# rep 3
for cfg in $CFGS; do cell $cfg V1 0.0001 300 3; done
for cfg in tatsbasic tats; do cell $cfg V3 0.0001 300 3; done
for cfg in $CFGS; do cell $cfg V1 0.001 90 3; done
for g in "V1 0.1 60" "V1 0.01 60" "V3 0.01 60"; do set -- $g; for cfg in $CFGS; do cell $cfg $1 $2 $3 3; done; done
docker compose -f "$HERE/docker-compose.raredrift.yml" down >/dev/null 2>&1
echo "ALL DONE $(date -u +%FT%TZ)"
