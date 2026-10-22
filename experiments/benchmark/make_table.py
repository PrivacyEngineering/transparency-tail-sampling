# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
"""Render results/benchmark/summary.json + benchmark_long.csv as Markdown tables (results_table.md).
Usage: python3 make_table.py OUT_DIR"""
import csv, json, math, statistics, sys
from collections import defaultdict
out = sys.argv[1]
S = json.load(open(f"{out}/summary.json"))
names = {"B0-none": "B0 pass-through", "B1-tailsampling-thesis": "B1 contrib tail_sampling (thesis)", "B2-transparency-thesis": "B2 transparency (thesis)",
         "B2off-transparency-thesis-checkoff": "B2-off (check off)", "B3-hardened": "B3 hardened", "B3off-hardened-checkoff": "B3-off (check off)"}
loads = sorted({int(l) for c in S for l in S[c]})
def f(m, k, d=3, scale=1.0):
    x = S[m[0]].get(str(m[1]), {}).get(k)
    return "-" if not x else f"{x['mean']*scale:.{d}g} ± {x['sd']*scale:.2g} (n={x['n']})" if x["n"] > 1 else f"{x['mean']*scale:.{d}g} (n=1)"
lines = ["# Benchmark results (laptop scale; mean ± sd over reps, n = number of reps)", ""]
for title, key, d, sc in [("CPU, mean cores over the 45 s steady window (collector limit 2 cores)", "cpu_mean", 3, 1), ("Memory, mean RSS (MiB, docker stats; limit 2048)", "mem_mean", 3, 1),
                          ("Exported spans/s (steady state) - budget cliff", "exported_spans_per_s", 4, 1), ("Policy decision latency p50 (ns, histogram interpolation)", "decision_latency_p50", 3, 1),
                          ("Policy decision latency p99 (ns, histogram interpolation)", "decision_latency_p99", 3, 1), ("Traces dropped before a decision (sampling_trace_dropped_too_early, traces/s)", "dropped_too_early_per_s", 3, 1),
                          ("Spans refused at the receiver (spans/s)", "refused_spans_per_s", 3, 1)]:
    lines += [f"## {title}", "", "| config | " + " | ".join(f"{l}" for l in loads) + " |", "|---|" + "---|" * len(loads)]
    for c in names:
        if c in S:
            lines.append(f"| {names[c]} | " + " | ".join(f((c, l), key, d, sc) for l in loads) + " |")
    lines.append("")
# paired check cost
by = defaultdict(dict)
for r in csv.DictReader(open(f"{out}/benchmark_long.csv")):
    if r["metric"] in ("cpu_mean", "decision_latency_p50", "mem_mean"):
        by[(r["metric"], r["offered_spans_per_s"], r["config"])][r["rep"]] = float(r["value"])
tcrit = {1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776}
lines += ["## Check cost (paired difference check-on minus check-off, same load and rep)", "", "| pair | load | metric | mean diff | 95 % CI | n |", "|---|---|---|---|---|---|"]
for on, off, nm in [("B2-transparency-thesis", "B2off-transparency-thesis-checkoff", "B2 vs B2-off"), ("B3-hardened", "B3off-hardened-checkoff", "B3 vs B3-off")]:
    for l in loads:
        for met, unit, sc in [("cpu_mean", "mcores", 1000), ("decision_latency_p50", "ns", 1)]:
            a, b = by.get((met, str(l), on), {}), by.get((met, str(l), off), {})
            d = [(a[r] - b[r]) * sc for r in a if r in b]
            if not d: continue
            m = statistics.mean(d)
            ci = f"[{m - tcrit.get(len(d)-1, 1.96)*statistics.stdev(d)/math.sqrt(len(d)):.3g}, {m + tcrit.get(len(d)-1, 1.96)*statistics.stdev(d)/math.sqrt(len(d)):.3g}]" if len(d) > 1 else "n/a"
            lines.append(f"| {nm} | {l} | {met} ({unit}) | {m:.3g} | {ci} | {len(d)} |")
open(f"{out}/results_table.md", "w").write("\n".join(lines) + "\n")
