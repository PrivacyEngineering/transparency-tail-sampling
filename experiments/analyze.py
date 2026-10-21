# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
"""Analyse one or more experiment runs (experiments/raw/<run_id>) and write tidy CSVs + JSON summary.

Usage: python analyze.py --raw experiments/raw --out experiments/results RUN_ID [RUN_ID ...]
Needs: python>=3.9, zstandard.

Ground truth = output of the non-sampling collector "none" (every span the shop emitted during the run,
after drift injection). Per trace we derive:
  has_pd           any span carries tilt.dataDisclosed.category
  viol_code        any span violates the conformity check *as implemented* in transparency_tag_filter.go:
                   span of a TILT-declared service (first matching dataDisclosed entry, by service.name) whose
                   category attribute is missing, empty, or not a substring of the declared category string
  viol_strict      as viol_code, but only spans that *carry* a category attribute (declared-category mismatch)
  drift            any span modified by the drift injection (experiment.drift_injected = true)
"""
import argparse, csv, gzip, json, math, os, re, statistics, struct, sys
from collections import defaultdict
import zstandard

CONFIGS = ["none", "head1", "head10", "tailprob", "tailpd", "tailthesis", "transpthesis", "transp", "transpstrict"]
CAT = "tilt.dataDisclosed.category"

def frames(path):
    raw = open(path, "rb").read()
    off, dctx = 0, zstandard.ZstdDecompressor()
    while off + 4 <= len(raw):
        n = struct.unpack(">I", raw[off:off + 4])[0]; off += 4
        if off + n > len(raw):
            print(f"warning: truncated frame in {path}", file=sys.stderr); break
        yield json.loads(dctx.decompress(raw[off:off + n], max_output_size=1 << 31)); off += n

def attrval(v):
    return next(iter(v.values())) if v else None

def spans(path):
    for msg in frames(path):
        for rs in msg.get("resourceSpans", []):
            svc = next((attrval(a["value"]) for a in rs["resource"].get("attributes", []) if a["key"] == "service.name"), None)
            for ss in rs.get("scopeSpans", []):
                for s in ss.get("spans", []):
                    attrs = {a["key"]: attrval(a.get("value", {})) for a in s.get("attributes", [])}
                    yield s["traceId"], s["spanId"], svc, attrs

def load_tilt(path):
    first = {}
    for e in json.load(open(path))["dataDisclosed"]:
        first.setdefault(e["_id"], e.get("category", "") or "")
    return first

def span_fails(declared, svc, attrs, strict):
    if svc not in declared:
        return False
    if CAT not in attrs:
        return not strict
    v = attrs[CAT]
    v = "" if v is None else str(v)
    return not (v != "" and v in declared[svc])

def ci95(xs):
    if len(xs) < 2: return (float("nan"), float("nan"))
    from statistics import mean, stdev
    t = {2: 12.706, 3: 4.303, 4: 3.182, 5: 2.776, 6: 2.571, 7: 2.447, 8: 2.365, 9: 2.306, 10: 2.262}.get(len(xs), 1.96)
    h = t * stdev(xs) / math.sqrt(len(xs))
    return (mean(xs) - h, mean(xs) + h)

def prom_hist(path, metric):
    """Return (sum, count) of a histogram over all label sets from a Prometheus text file."""
    s = c = 0.0
    if not os.path.exists(path): return (float("nan"), float("nan"))
    for line in open(path):
        if line.startswith(metric + "_sum"): s += float(line.rsplit(" ", 1)[1])
        elif line.startswith(metric + "_count"): c += float(line.rsplit(" ", 1)[1])
    return (s, c)

def prom_counter(path, metric, label_filter=""):
    v = 0.0
    if not os.path.exists(path): return float("nan")
    for line in open(path):
        if line.startswith(metric + "{") and label_filter in line: v += float(line.rsplit(" ", 1)[1])
    return v

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True); ap.add_argument("--out", required=True)
    ap.add_argument("--tilt", default=os.path.join(os.path.dirname(__file__), "..", "tilt", "tilt.corrected.json"))
    ap.add_argument("runs", nargs="+")
    a = ap.parse_args()
    declared = load_tilt(a.tilt)
    os.makedirs(os.path.join(a.out, "traces"), exist_ok=True)
    long_rows, res_rows = [], []
    for run in a.runs:
        rd = os.path.join(a.raw, run)
        # ---- ground truth
        gt = defaultdict(lambda: {"n": 0, "pd": False, "vc": False, "vs": False, "drift": False, "svcs": set()})
        span_ids = set()
        for tid, sid, svc, attrs in spans(os.path.join(rd, "sink", "none.jsonl.zst")):
            if (tid, sid) in span_ids: continue
            span_ids.add((tid, sid))
            t = gt[tid]; t["n"] += 1; t["svcs"].add(svc or "")
            if CAT in attrs: t["pd"] = True
            if attrs.get("experiment.drift_injected") is True: t["drift"] = True
            if span_fails(declared, svc, attrs, strict=False): t["vc"] = True
            if span_fails(declared, svc, attrs, strict=True): t["vs"] = True
        with gzip.open(os.path.join(a.out, "traces", f"ground_truth_traces_{run}.csv.gz"), "wt", newline="") as f:
            w = csv.writer(f); w.writerow(["run_id", "trace_id", "n_spans", "has_pd", "viol_code", "viol_strict", "drift_injected", "services"])
            for tid, t in gt.items():
                w.writerow([run, tid, t["n"], int(t["pd"]), int(t["vc"]), int(t["vs"]), int(t["drift"]), ";".join(sorted(t["svcs"]))])
        sets = {"all": set(gt), "pd": {k for k, t in gt.items() if t["pd"]}, "viol_code": {k for k, t in gt.items() if t["vc"]},
                "viol_strict": {k for k, t in gt.items() if t["vs"]}, "drift": {k for k, t in gt.items() if t["drift"]}}
        total_spans = sum(t["n"] for t in gt.values())
        # ---- load window for resource statistics
        t0 = float(open(os.path.join(rd, "load_start_unix")).read()); t1 = float(open(os.path.join(rd, "load_end_unix")).read())
        stats = defaultdict(list)
        for r in csv.DictReader(open(os.path.join(rd, "docker_stats.csv"))):
            res_rows.append(r)
            if t0 + 30 <= float(r["unix_time_s"]) <= t1:  # first 30 s (user ramp-up) discarded
                stats[r["container"]].append((float(r["cpu_percent"]), float(r["mem_mib"])))
        for cfg in CONFIGS:
            kept = defaultdict(int)
            seen = set()
            for tid, sid, svc, attrs in spans(os.path.join(rd, "sink", f"{cfg}.jsonl.zst")):
                if (tid, sid) in seen: continue
                seen.add((tid, sid)); kept[tid] += 1
            kept_ids = set(kept)
            kept_in_gt = kept_ids & sets["all"]
            m = {
                "traces_total": (len(sets["all"]), "traces"), "spans_total": (total_spans, "spans"),
                "traces_retained": (len(kept_ids), "traces"), "spans_retained": (sum(kept.values()), "spans"),
                "traces_pd_total": (len(sets["pd"]), "traces"), "traces_viol_code_total": (len(sets["viol_code"]), "traces"),
                "traces_viol_strict_total": (len(sets["viol_strict"]), "traces"), "traces_drift_total": (len(sets["drift"]), "traces"),
                "trace_retention": (len(kept_in_gt) / len(sets["all"]), "fraction"),
                "span_retention": (sum(kept.values()) / total_spans, "fraction"),
                "complete_trace_share": (sum(1 for k in kept_in_gt if kept[k] >= gt[k]["n"]) / max(1, len(kept_in_gt)), "fraction"),
                "retained_not_in_ground_truth": (len(kept_ids - sets["all"]), "traces"),
            }
            for key in ["pd", "viol_code", "viol_strict", "drift"]:
                tot = sets[key]
                m[f"recall_{key}"] = (len(tot & kept_ids) / len(tot) if tot else float("nan"), "fraction")
                m[f"precision_{key}"] = (len(tot & kept_ids) / len(kept_in_gt) if kept_in_gt else float("nan"), "fraction")
            st = stats.get(f"c-{cfg}", [])
            if st:
                m["cpu_mean"] = (statistics.mean(x for x, _ in st) / 100, "cores")
                m["cpu_p95"] = (sorted(x for x, _ in st)[int(0.95 * (len(st) - 1))] / 100, "cores")
                m["mem_mean"] = (statistics.mean(y for _, y in st), "MiB")
                m["mem_max"] = (max(y for _, y in st), "MiB")
            prom = os.path.join(rd, f"metrics_{cfg}.prom")
            for proc in ["transparency", "tail_sampling"]:
                s, c = prom_hist(prom, f"otelcol_processor_{proc}_sampling_decision_latency")
                if c and c == c and proc == ("transparency" if cfg.startswith("transp") else "tail_sampling"):
                    m["policy_decision_latency_mean"] = (s / c, "microseconds")
                    m["policy_evaluations"] = (c, "count")
            m["receiver_accepted_spans"] = (prom_counter(prom, "otelcol_receiver_accepted_spans"), "spans")
            m["exporter_sent_spans"] = (prom_counter(prom, "otelcol_exporter_sent_spans"), "spans")
            m["dropped_too_early"] = (prom_counter(prom, "otelcol_processor_tail_sampling_sampling_trace_dropped_too_early"), "traces")
            for k, (v, u) in m.items():
                long_rows.append({"run_id": run, "config": cfg, "metric": k, "value": v, "unit": u})
        print(f"{run}: {len(gt)} traces, {total_spans} spans, pd={len(sets['pd'])} viol_code={len(sets['viol_code'])} viol_strict={len(sets['viol_strict'])} drift={len(sets['drift'])}", file=sys.stderr)
    with open(os.path.join(a.out, "metrics_long.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["run_id", "config", "metric", "value", "unit"]); w.writeheader(); w.writerows(long_rows)
    with open(os.path.join(a.out, "docker_stats_long.csv"), "w", newline="") as f:
        w = csv.DictWriter(f, fieldnames=["unix_time_s", "run_id", "container", "cpu_percent", "mem_mib"]); w.writeheader(); w.writerows(res_rows)
    summ = defaultdict(dict)
    by = defaultdict(list); units = {}
    for r in long_rows:
        if r["value"] == r["value"]:
            by[(r["config"], r["metric"])].append(r["value"]); units[r["metric"]] = r["unit"]
    for (cfg, met), xs in by.items():
        lo, hi = ci95(xs)
        summ[cfg][met] = {"mean": statistics.mean(xs), "sd": statistics.stdev(xs) if len(xs) > 1 else 0.0, "n": len(xs),
                          "min": min(xs), "max": max(xs), "ci95_low": lo, "ci95_high": hi, "unit": units[met]}
    json.dump({"runs": a.runs, "configs": summ}, open(os.path.join(a.out, "summary.json"), "w"), indent=1, default=str)

if __name__ == "__main__":
    main()
