"""Analyse benchmark raw data (experiments/raw/benchmark/<config>/<load>/rep<k>) -> tidy CSV + JSON summary.

Usage: python3 analyze_benchmark.py RAW_DIR OUT_DIR
All rates are computed over the steady-state window [steady_start, steady_end] (warm-up discarded) from
deltas of the collector's own Prometheus counters; CPU/memory from `docker stats` samples (~1/s) in that window.
"""
import csv, glob, json, math, os, statistics, sys
from collections import defaultdict


def prom(path):
    out = defaultdict(float)
    if not os.path.exists(path):
        return out
    for line in open(path):
        if line.startswith("#") or not line.strip():
            continue
        nl, _, v = line.rstrip().rpartition(" ")
        name, _, labels = nl.partition("{")
        labels = ",".join(p for p in labels.rstrip("}").split(",") if p and not p.startswith(("service_instance_id", "service_name", "service_version")))
        try:
            out[(name, labels)] += float(v)
        except ValueError:
            pass
    return out


def total(d, name, contains=""):
    return sum(v for (n, l), v in d.items() if n == name and contains in l)


def hist_delta_quantiles(start, end, base, qs=(0.5, 0.99)):
    b = defaultdict(float)
    for (n, l), v in end.items():
        if n == base + "_bucket":
            le = [p.split("=")[1].strip('"') for p in l.split(",") if p.startswith("le=")][0]
            b[float("inf") if le == "+Inf" else float(le)] += v - start.get((n, l), 0.0)
    if not b:
        return {q: float("nan") for q in qs}, 0.0
    les = sorted(b)
    tot = b[les[-1]]
    res = {}
    for q in qs:
        res[q] = float("nan")
        target, pl, pc = q * tot, 0.0, 0.0
        for le in les:
            c = b[le]
            if tot > 0 and c >= target:
                res[q] = pl if math.isinf(le) else pl + ((target - pc) / (c - pc) if c > pc else 1.0) * (le - pl)
                break
            pl, pc = le, c
    return res, tot


def ci95(xs):
    if len(xs) < 2:
        return float("nan"), float("nan")
    t = {2: 12.706, 3: 4.303, 4: 3.182, 5: 2.776, 6: 2.571, 7: 2.447, 8: 2.365, 9: 2.306, 10: 2.262}.get(len(xs), 1.96)
    h = t * statistics.stdev(xs) / math.sqrt(len(xs))
    return statistics.mean(xs) - h, statistics.mean(xs) + h


def main(raw, out):
    os.makedirs(out, exist_ok=True)
    rows, stat_rows = [], []
    for d in sorted(glob.glob(os.path.join(raw, "*", "*", "rep*"))):
        if not os.path.exists(os.path.join(d, "done.txt")):
            continue
        rep = os.path.basename(d); load = int(os.path.basename(os.path.dirname(d))); cfg = os.path.basename(os.path.dirname(os.path.dirname(d)))
        t0 = float(open(os.path.join(d, "steady_start")).read()); t1 = float(open(os.path.join(d, "steady_end")).read()); dt = t1 - t0
        s, e = prom(os.path.join(d, "metrics_start.prom")), prom(os.path.join(d, "metrics_end.prom"))
        gen = json.load(open(os.path.join(d, "generator.json"))) if os.path.getsize(os.path.join(d, "generator.json")) else {}
        cpu, mem = [], []
        for r in csv.DictReader(open(os.path.join(d, "docker_stats.csv"))):
            stat_rows.append({"config": cfg, "offered_spans_per_s": load, "rep": rep, "unix_time_s": r["unix_time_s"],
                              "t_rel_steady_start_s": round(float(r["unix_time_s"]) - t0, 2), "cpu_cores": r["cpu_cores"], "mem_mib": r["mem_mib"]})
            if t0 <= float(r["unix_time_s"]) <= t1:
                cpu.append(float(r["cpu_cores"])); mem.append(float(r["mem_mib"]))
        def rate(name, contains=""):
            return (total(e, name, contains) - total(s, name, contains)) / dt
        m = {
            "offered_spans_per_s": (load, "spans/s"),
            "generator_achieved_spans_per_s": (gen.get("achieved_spans_per_s", float("nan")), "spans/s"),
            "generator_failed_spans": (gen.get("failed_spans", float("nan")), "spans"),
            "accepted_spans_per_s": (rate("otelcol_receiver_accepted_spans"), "spans/s"),
            "refused_spans_per_s": (rate("otelcol_receiver_refused_spans"), "spans/s"),
            "exported_spans_per_s": (rate("otelcol_exporter_sent_spans"), "spans/s"),
            "export_failed_spans_per_s": (rate("otelcol_exporter_send_failed_spans") + rate("otelcol_exporter_enqueue_failed_spans"), "spans/s"),
            "processor_dropped_spans_per_s": (rate("otelcol_processor_dropped_spans"), "spans/s"),
            "exporter_queue_size_end": (total(e, "otelcol_exporter_queue_size"), "batches"),
            "dropped_too_early_per_s": (max(rate(f"otelcol_processor_{p}_sampling_trace_dropped_too_early") for p in ("transparency", "tail_sampling")), "traces/s"),
            "evicted_undecided_per_s": (rate("otelcol_processor_transparency_tp_traces_evicted_undecided"), "traces/s"),
            "traces_sampled_per_s": (max(rate(f"otelcol_processor_{p}_count_traces_sampled", 'sampled="true"') for p in ("transparency", "tail_sampling")), "traces/s"),
            "traces_not_sampled_per_s": (max(rate(f"otelcol_processor_{p}_count_traces_sampled", 'sampled="false"') for p in ("transparency", "tail_sampling")), "traces/s"),
            "traces_on_memory_end": (max(total(e, f"otelcol_processor_{p}_sampling_traces_on_memory") for p in ("transparency", "tail_sampling")), "traces"),
            "process_rss_end": (total(e, "otelcol_process_memory_rss") / 2**20, "MiB"),
            "cpu_mean": (statistics.mean(cpu) if cpu else float("nan"), "cores"),
            "cpu_p95": (sorted(cpu)[int(0.95 * (len(cpu) - 1))] if cpu else float("nan"), "cores"),
            "mem_mean": (statistics.mean(mem) if mem else float("nan"), "MiB"),
            "mem_max": (max(mem) if mem else float("nan"), "MiB"),
            "stats_samples": (len(cpu), "count"),
        }
        q, n = hist_delta_quantiles(s, e, "otelcol_processor_transparency_tp_sampling_decision_latency_ns")
        if n > 0:
            m["decision_latency_p50"] = (q[0.5], "ns")
            m["decision_latency_p99"] = (q[0.99], "ns")
            m["decision_latency_source"] = (1, "1=ns histogram (transparency processor)")
        else:  # contrib tail_sampling records only the µs-truncated histogram
            q, n = hist_delta_quantiles(s, e, "otelcol_processor_tail_sampling_sampling_decision_latency")
            if n > 0:
                m["decision_latency_p50"] = (q[0.5] * 1000, "ns")
                m["decision_latency_p99"] = (q[0.99] * 1000, "ns")
                m["decision_latency_source"] = (2, "2=us histogram (contrib, truncated to whole us)")
        m["decisions_per_s"] = (n / dt, "policy evaluations/s")
        acc = m["accepted_spans_per_s"][0]
        m["saturated"] = (1.0 if (acc < 0.95 * load or m["refused_spans_per_s"][0] > 0 or (gen.get("failed_spans") or 0) > 0) else 0.0, "bool")
        for k, (v, u) in m.items():
            rows.append({"config": cfg, "offered_spans_per_s": load, "rep": rep, "metric": k, "value": v, "unit": u})
    with open(os.path.join(out, "benchmark_long.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["config", "offered_spans_per_s", "rep", "metric", "value", "unit"]); w.writeheader(); w.writerows(rows)
    with open(os.path.join(out, "benchmark_docker_stats_long.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["config", "offered_spans_per_s", "rep", "unix_time_s", "t_rel_steady_start_s", "cpu_cores", "mem_mib"]); w.writeheader(); w.writerows(stat_rows)
    by, units = defaultdict(list), {}
    for r in rows:
        if r["value"] == r["value"]:
            by[(r["config"], r["offered_spans_per_s"], r["metric"])].append(r["value"]); units[r["metric"]] = r["unit"]
    summ = defaultdict(lambda: defaultdict(dict))
    for (c, l, mname), xs in by.items():
        lo, hi = ci95(xs)
        summ[c][l][mname] = {"mean": statistics.mean(xs), "sd": statistics.stdev(xs) if len(xs) > 1 else 0.0, "n": len(xs),
                             "ci95_low": lo, "ci95_high": hi, "unit": units[mname]}
    json.dump(summ, open(os.path.join(out, "benchmark_summary.json"), "w"), indent=1)


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
