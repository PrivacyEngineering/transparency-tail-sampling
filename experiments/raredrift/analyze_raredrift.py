# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
"""Analyse the rare-drift sweep: RAW_DIR/<config>/<class>/p<P>/rep<k>/ -> tidy long CSV, per-event CSV, summary.json.

Usage: python3 analyze_raredrift.py RAW_DIR OUT_DIR [WINDOW_S=60]
Per cell (config, drift class, p, rep):
  injected / detected (>=1 retained trace carrying the injected drift), recall = detected/injected,
  detection probability within fixed non-overlapping windows of WINDOW_S seconds (a window counts if it contains >=1 injected
  event; it is a hit if >=1 of ITS events was retained), time-to-first-detection (arrival at the sink of the first retained
  drift trace, seconds after load start; censored = no drift trace retained, value = horizon duration+drain, censored=1),
  per-event detection delay (sink arrival - send time), retained traces/s and spans/s, share of retained traces that are drift
  evidence, collector CPU (cores) and RSS (MiB) while the generator runs (first 10 s skipped).
Analytic baseline for sampling rate r: window detection probability 1-(1-r)^k with the cell's actual k events per window.
"""
import csv, glob, gzip, json, math, os, statistics, sys
from collections import defaultdict

LABEL = {"head1": "Head 1%", "head10": "Head 10%", "tailrandom": "Tail random", "tailattr": "Tail attribute",
         "tatsbasic": "TATS-basic", "tats": "TATS"}
ORDER = list(LABEL)
T975 = {1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571, 6: 2.447, 7: 2.365, 8: 2.306, 9: 2.262}


def ci95(xs):
    if len(xs) < 2:
        return float("nan"), float("nan")
    h = T975.get(len(xs) - 1, 1.96) * statistics.stdev(xs) / math.sqrt(len(xs))
    m = statistics.mean(xs)
    return m - h, m + h


def wilson(k, n, z=1.96):
    if n == 0:
        return float("nan"), float("nan")
    p = k / n
    d = 1 + z * z / n
    c = (p + z * z / (2 * n)) / d
    h = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / d
    return c - h, c + h


def cell(d, window):
    gen = json.load(open(os.path.join(d, "generator.json")))
    t0 = gen["start_unix_ns"] / 1e9
    dur = gen["elapsed_s"]
    drain = float(os.environ.get("DRAIN", 38))
    gt = {}
    for line in open(os.path.join(d, "groundtruth.tsv")):
        tid, _, t = line.rstrip("\n").split("\t")
        gt[tid] = int(t) / 1e9 - t0
    ret_first, ret_spans = {}, defaultdict(int)
    for line in open(os.path.join(d, "retained.tsv")):
        t, tid, n = line.rstrip("\n").split("\t")
        t = int(t) / 1e9 - t0
        if tid not in ret_first or t < ret_first[tid]:
            ret_first[tid] = t
        ret_spans[tid] += int(n)
    events = [(tid, ts, tid in ret_first, (ret_first[tid] - ts) if tid in ret_first else float("nan")) for tid, ts in sorted(gt.items(), key=lambda x: x[1])]
    inj, det = len(events), sum(1 for e in events if e[2])
    m = {"sent_traces": (gen["sent_traces"], "traces"), "sent_spans": (gen["sent_spans"], "spans"),
         "achieved_spans_per_s": (gen["achieved_spans_per_s"], "spans/s"), "failed_spans": (gen["failed_spans"], "spans"),
         "injected": (inj, "traces"), "detected": (det, "traces"),
         "recall": (det / inj if inj else float("nan"), "fraction")}
    dts = [ret_first[e[0]] for e in events if e[2]]
    m["ttfd_s"] = (min(dts) if dts else dur + drain, "s")
    m["ttfd_censored"] = (0.0 if dts else 1.0, "bool")
    m["horizon_s"] = (dur + drain, "s")
    dl = [e[3] for e in events if e[2]]
    m["detect_delay_median_s"] = (statistics.median(dl) if dl else float("nan"), "s")
    # fixed windows
    nw = int(dur // window)
    ks, hits = [], []
    for w in range(nw):
        ev = [e for e in events if w * window <= e[1] < (w + 1) * window]
        if ev:
            ks.append(len(ev)); hits.append(1.0 if any(e[2] for e in ev) else 0.0)
    m["windows_with_events"] = (len(ks), "count")
    m["events_per_window_mean"] = (statistics.mean(ks) if ks else float("nan"), "events")
    m["window_detect_prob"] = (statistics.mean(hits) if hits else float("nan"), "fraction")
    for r in (0.01, 0.10, 0.15):
        m[f"analytic_window_detect_r{r:g}"] = (statistics.mean(1 - (1 - r) ** k for k in ks) if ks else float("nan"), "fraction")
    # volume
    rt, rs = len(ret_first), sum(ret_spans.values())
    m["retained_traces_per_s"] = (rt / dur, "traces/s")
    m["retained_spans_per_s"] = (rs / dur, "spans/s")
    m["retained_span_share"] = (rs / gen["sent_spans"] if gen["sent_spans"] else float("nan"), "fraction")
    m["drift_share_of_retained"] = (det / rt if rt else float("nan"), "fraction")
    # resources
    cpu, mem = [], []
    for r in csv.DictReader(open(os.path.join(d, "docker_stats.csv"))):
        t = float(r["unix_time_s"])
        if t0 + 10 <= t <= t0 + dur:
            cpu.append(float(r["cpu_cores"])); mem.append(float(r["mem_mib"]))
    m["cpu_cores_mean"] = (statistics.mean(cpu) if cpu else float("nan"), "cores")
    m["rss_mib_mean"] = (statistics.mean(mem) if mem else float("nan"), "MiB")
    m["rss_mib_max"] = (max(mem) if mem else float("nan"), "MiB")
    # collector counters
    refused = 0.0
    for line in open(os.path.join(d, "metrics_end.prom")) if os.path.exists(os.path.join(d, "metrics_end.prom")) else []:
        if line.startswith("otelcol_receiver_refused_spans"):
            refused += float(line.rsplit(" ", 1)[1])
    m["refused_spans"] = (refused, "spans")
    return m, events


def main(raw, out, window=60):
    os.makedirs(out, exist_ok=True)
    rows, evrows = [], []
    for d in sorted(glob.glob(os.path.join(raw, "*", "V*", "p*", "rep*"))):
        if not os.path.exists(os.path.join(d, "done.txt")):
            continue
        rep = int(d.rsplit("rep", 1)[1]); p = float(os.path.basename(os.path.dirname(d))[1:])
        cls = os.path.basename(os.path.dirname(os.path.dirname(d))); cfg = os.path.basename(os.path.dirname(os.path.dirname(os.path.dirname(d))))
        m, events = cell(d, window)
        for k, (v, u) in m.items():
            rows.append({"config": cfg, "label": LABEL[cfg], "drift_class": cls, "p": p, "rep": rep, "metric": k, "value": v, "unit": u})
        for tid, ts, dt, dl in events:
            evrows.append({"config": cfg, "drift_class": cls, "p": p, "rep": rep, "trace_id": tid, "t_sent_s": round(ts, 3),
                           "retained": int(dt), "delay_s": "" if dl != dl else round(dl, 3)})
    with open(os.path.join(out, "raredrift_long.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["config", "label", "drift_class", "p", "rep", "metric", "value", "unit"]); w.writeheader(); w.writerows(rows)
    with gzip.open(os.path.join(out, "raredrift_events.csv.gz"), "wt", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["config", "drift_class", "p", "rep", "trace_id", "t_sent_s", "retained", "delay_s"]); w.writeheader(); w.writerows(evrows)
    by, units, lab = defaultdict(list), {}, {}
    for r in rows:
        if r["value"] == r["value"]:
            by[(r["config"], r["drift_class"], r["p"], r["metric"])].append(r["value"]); units[r["metric"]] = r["unit"]; lab[r["config"]] = r["label"]
    summ = {}
    for (c, cls, p, mname), xs in sorted(by.items(), key=lambda kv: (ORDER.index(kv[0][0]), kv[0][1], -kv[0][2], kv[0][3])):
        lo, hi = ci95(xs)
        cellk = summ.setdefault(c, {"label": lab[c], "cells": {}})["cells"].setdefault(f"{cls}/p={p:g}", {})
        cellk[mname] = {"mean": statistics.mean(xs), "sd": statistics.stdev(xs) if len(xs) > 1 else float("nan"), "n": len(xs),
                        "ci95_low": lo, "ci95_high": hi, "unit": units[mname]}
        if mname == "recall":  # pooled over reps with Wilson interval (events are the Bernoulli trials)
            i = sum(r["value"] for r in rows if r["config"] == c and r["drift_class"] == cls and r["p"] == p and r["metric"] == "injected")
            k = sum(r["value"] for r in rows if r["config"] == c and r["drift_class"] == cls and r["p"] == p and r["metric"] == "detected")
            wl, wh = wilson(k, i)
            cellk["recall_pooled"] = {"injected": i, "detected": k, "recall": k / i if i else float("nan"), "wilson95_low": wl, "wilson95_high": wh}
    json.dump(summ, open(os.path.join(out, "summary.json"), "w"), indent=1)
    # compact markdown table for README / notes
    with open(os.path.join(out, "summary_table.md"), "w") as f:
        f.write("| config | class | p | reps | injected (pooled) | recall pooled [Wilson 95%] | P(detect in 60 s window) | analytic r=1% / 10% / 15% | TTFD mean over reps (s) [censored cells] | retained spans/s | drift share of retained | CPU cores | RSS MiB |\n|" + "---|" * 13 + "\n")
        for c in ORDER:
            for key, cm in summ.get(c, {"cells": {}})["cells"].items():
                g = lambda n: cm[n]["mean"] if n in cm else float("nan")
                rp = cm["recall_pooled"]
                cens = sum(1 for r in rows if r["config"] == c and f"{r['drift_class']}/p={r['p']:g}" == key and r["metric"] == "ttfd_censored" and r["value"] == 1)
                cls, p = key.split("/p=")
                f.write(f"| {summ[c]['label']} | {cls} | {p} | {cm['recall']['n']} | {rp['injected']:.0f} | {rp['recall']:.3f} [{rp['wilson95_low']:.3f}, {rp['wilson95_high']:.3f}] | "
                        f"{g('window_detect_prob'):.3f} | {g('analytic_window_detect_r0.01'):.3f} / {g('analytic_window_detect_r0.1'):.3f} / {g('analytic_window_detect_r0.15'):.3f} | "
                        f"{g('ttfd_s'):.1f} [{cens}/{cm['recall']['n']}] | {g('retained_spans_per_s'):.0f} | {g('drift_share_of_retained'):.4f} | {g('cpu_cores_mean'):.3f} | {g('rss_mib_mean'):.0f} |\n")


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2], float(sys.argv[3]) if len(sys.argv) > 3 else 60)
