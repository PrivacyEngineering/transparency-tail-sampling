#!/usr/bin/env bash
# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
# Full benchmark driver used for the reported results (see experiments/README.md). Wall-clock cap for phase 3: DEADLINE_MIN.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); R="$HERE/run_benchmark.sh"
ALL="B0-none B1-tailsampling-thesis B2-transparency-thesis B2off-transparency-thesis-checkoff B3-hardened B3off-hardened-checkoff"
START=$(date +%s); DEADLINE=$((START + ${DEADLINE_MIN:-150}*60))
# Phase 1: 6 configs x {1k,2k,5k,10k} spans/s, 2 reps
"$R" 2 "$ALL" "1000 2000 5000 10000"
# Phase 2: 500 and 20k spans/s, 1 rep
"$R" 1 "$ALL" "500 20000"
# Phase 3: third rep of the main grid while time remains (single cell per call so the deadline is honoured)
for load in 2000 10000 5000 1000; do for cfg in B2-transparency-thesis B2off-transparency-thesis-checkoff B3-hardened B3off-hardened-checkoff B1-tailsampling-thesis B0-none; do
  [ "$(date +%s)" -gt $((DEADLINE - 130)) ] && exit 0
  "$R" 3 "$cfg" "$load"
done; done
