"""Analyse experiment v2 runs (experiments/raw/v2/<run_id>) -> tidy CSVs + JSON summary.

Usage: python analyze_v2.py --raw experiments/raw/v2 --out experiments/results/v2 [--tilt tilt/tilt.corrected.json] RUN_ID...
Needs python>=3.9 and zstandard.

Ground truth (gt) = every span received by the fan-out collector after drift injection, tapped before any
memory_limiter or sampler. Per trace:
  inj_V1/inj_V2/inj_V3  trace contains a span modified by the drift injection of that class (experiment.drift_class)
  nat_V2                trace contains an un-injected span of a TILT-listed service without category annotation
                        (typically auto-instrumented spans) - "noise" under the thesis semantics
  has_pd                any span carries tilt.dataDisclosed.category
  viol_thesis           as the thesis code decides (substring match, first TILT entry, missing annotation = fail)
  viol_hardened         as tilt_conformity decides by default (exact set membership, unlisted annotated service = V3,
                        missing annotation NOT counted)
"""
import argparse, csv, gzip, json, math, os, statistics, struct, sys
from collections import defaultdict
import zstandard

CONFIGS = ["none", "head1", "head10", "tailprob", "tailthesis", "transpthesis", "transp", "hardened", "hardenedcomp"]
CAT = "tilt.dataDisclosed.category"
CLASSES = ["inj_V1", "inj_V2", "inj_V3", "nat_V2", "has_pd", "viol_thesis", "viol_hardened"]
WINDOW_S = 60


def frames(path):
    if not os.path.exists(path):
        return
    raw = open(path, "rb").read()
    off, d = 0, zstandard.ZstdDecompressor()
    while off + 4 <= len(raw):
        n = struct.unpack(">I", raw[off:off + 4])[0]; off += 4
        if off + n > len(raw):
            print(f"warning: truncated frame in {path}", file=sys.stderr); break
        yield json.loads(d.decompress(raw[off:off + n], max_output_size=1 << 31)); off += n


def val(v):
    return next(iter(v.values())) if v else None


def spans(path):
    for msg in frames(path):
        for rs in msg.get("resourceSpans", []):
            svc = next((val(a["value"]) for a in rs["resource"].get("attributes", []) if a["key"] == "service.name"), "")
            for ss in rs.get("scopeSpans", []):
                for s in ss.get("spans", []):
                    yield s, svc, {a["key"]: val(a.get("value", {})) for a in s.get("attributes", [])}


def load_tilt(path):
    doc = json.load(open(path))["dataDisclosed"]
    first, union = {}, defaultdict(set)
    for e in doc:
        c = e.get("category", "") or ""
        first.setdefault(e["_id"], c)
        union[e["_id"]].update(x.strip() for x in c.split(";") if x.strip())
    return first, union


def thesis_fails(first, svc, attrs):
    if svc not in first:
        return False
    if CAT not in attrs:
        return True
    v = "" if attrs[CAT] is None else str(attrs[CAT])
    return not (v != "" and v in first[svc])


def hardened_fails(union, svc, attrs):
    cat = str(attrs.get(CAT) or "").strip()
    if svc not in union:
        return cat != ""
    return cat != "" and cat not in union[svc]


def prom(path):
    """Parse a Prometheus text file into list of (name, labels-string, value)."""
    out = []
    if not os.path.exists(path):
        return out
    for line in open(path):
        if line.startswith("#") or not line.strip():
            continue
        name_labels, _, v = line.rstrip().rpartition(" ")
        name, _, labels = name_labels.partition("{")
        try:
            out.append((name, labels, float(v)))
        except ValueError:
            pass
    return out


def psum(rows, name, contains=""):
    return sum(v for n, l, v in rows if n == name and contains in l)


def hist_quantiles(rows, base, qs=(0.5, 0.99)):
    """Quantiles (upper bucket bound, linear interpolation inside bucket) of a cumulative Prometheus histogram,
    summed over label sets."""
    buckets = defaultdict(float)
    for n, l, v in rows:
        if n == base + "_bucket":
            le = [p.split("=")[1].strip('"') for p in l.rstrip("}").split(",") if p.startswith("le=")][0]
            buckets[float("inf") if le == "+Inf" else float(le)] += v
    if not buckets:
        return {q: float("nan") for q in qs}
    les = sorted(buckets)
    total = buckets[les[-1]]
    res = {}
    for q in qs:
        target, prev_le, prev_c = q * total, 0.0, 0.0
        res[q] = float("nan")
        for le in les:
            c = buckets[le]
            if c >= target and total > 0:
                if math.isinf(le):
                    res[q] = prev_le
                else:
                    frac = (target - prev_c) / (c - prev_c) if c > prev_c else 1.0
                    res[q] = prev_le + frac * (le - prev_le)
                break
            prev_le, prev_c = le, c
    return res


def ci95(xs):
    if len(xs) < 2:
        return (float("nan"), float("nan"))
    t = {2: 12.706, 3: 4.303, 4: 3.182, 5: 2.776, 6: 2.571, 7: 2.447, 8: 2.365, 9: 2.306, 10: 2.262}.get(len(xs), 1.96)
    h = t * statistics.stdev(xs) / math.sqrt(len(xs))
    return (statistics.mean(xs) - h, statistics.mean(xs) + h)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--raw", required=True); ap.add_argument("--out", required=True)
    ap.add_argument("--tilt", default=os.path.join(os.path.dirname(__file__), "..", "..", "tilt", "tilt.corrected.json"))
    ap.add_argument("--scenario", default="clean_tilt_injected_drift")
    ap.add_argument("runs", nargs="+")
    a = ap.parse_args()
    first, union = load_tilt(a.tilt)
    os.makedirs(os.path.join(a.out, "traces"), exist_ok=True)
    rows, det_rows, res_rows = [], [], []
    for run in a.runs:
        rd = os.path.join(a.raw, run)
        gt = defaultdict(lambda: {"n": 0, "t0": float("inf"), **{c: False for c in CLASSES}})
        seen = set()
        for s, svc, at in spans(os.path.join(rd, "sink", "gt.jsonl.zst")):
            k = (s["traceId"], s["spanId"])
            if k in seen:
                continue
            seen.add(k)
            t = gt[s["traceId"]]
            t["n"] += 1
            t["t0"] = min(t["t0"], int(s.get("startTimeUnixNano", 0)) / 1e9)
            dc = at.get("experiment.drift_class")
            if dc in ("V1", "V2", "V3"):
                t["inj_" + dc] = True
            if CAT in at:
                t["has_pd"] = True
            if svc in first and CAT not in at and dc != "V2":
                t["nat_V2"] = True
            if thesis_fails(first, svc, at):
                t["viol_thesis"] = True
            if hardened_fails(union, svc, at):
                t["viol_hardened"] = True
        load_t0 = float(open(os.path.join(rd, "load_start_unix")).read())
        load_t1 = float(open(os.path.join(rd, "load_end_unix")).read())
        with gzip.open(os.path.join(a.out, "traces", f"gt_traces_{run}.csv.gz"), "wt", newline="") as f:
            w = csv.writer(f)
            w.writerow(["run_id", "trace_id", "start_s_rel_load", "n_spans"] + CLASSES)
            for tid, t in gt.items():
                w.writerow([run, tid, round(t["t0"] - load_t0, 3), t["n"]] + [int(t[c]) for c in CLASSES])
        total_spans = sum(t["n"] for t in gt.values())
        sets = {c: {k for k, t in gt.items() if t[c]} for c in CLASSES}
        sets["all"] = set(gt)
        noise = {k for k, t in gt.items() if t["nat_V2"] and not (t["inj_V1"] or t["inj_V2"] or t["inj_V3"] or t["viol_hardened"])}
        stats = defaultdict(list)
        for r in csv.DictReader(open(os.path.join(rd, "docker_stats.csv"))):
            res_rows.append(r)
            if load_t0 + 30 <= float(r["unix_time_s"]) <= load_t1:
                stats[r["container"]].append((float(r["cpu_percent"]), float(r["mem_mib"])))
        for cfg in CONFIGS:
            kept, kseen = defaultdict(int), set()
            for s, svc, at in spans(os.path.join(rd, "sink", f"{cfg}.jsonl.zst")):
                k = (s["traceId"], s["spanId"])
                if k in kseen:
                    continue
                kseen.add(k); kept[s["traceId"]] += 1
            K = set(kept) & sets["all"]
            kept_spans = sum(kept[k] for k in K)
            m = {"traces_total": (len(gt), "traces"), "spans_total": (total_spans, "spans"),
                 "traces_retained": (len(K), "traces"), "spans_retained": (kept_spans, "spans"),
                 "trace_retention": (len(K) / len(gt), "fraction"), "span_retention": (kept_spans / total_spans, "fraction"),
                 "retained_not_in_gt": (len(set(kept) - sets["all"]), "traces"),
                 "complete_trace_share": (sum(1 for k in K if kept[k] >= gt[k]["n"]) / max(1, len(K)), "fraction")}
            for c in CLASSES:
                m[f"n_{c}"] = (len(sets[c]), "traces")
                m[f"recall_{c}"] = (len(sets[c] & K) / len(sets[c]) if sets[c] else float("nan"), "fraction")
                m[f"precision_{c}"] = (len(sets[c] & K) / len(K) if K else float("nan"), "fraction")
            m["budget_share_nat_V2_noise"] = (sum(kept[k] for k in K & noise) / kept_spans if kept_spans else float("nan"), "fraction of retained spans")
            inj = sets["inj_V1"] | sets["inj_V2"] | sets["inj_V3"]
            m["budget_share_injected_drift"] = (sum(kept[k] for k in K & inj) / kept_spans if kept_spans else float("nan"), "fraction of retained spans")
            # detection per injected class: time to first detection and per-window detection probability
            for c in ["inj_V1", "inj_V2", "inj_V3"]:
                occ = sorted(gt[k]["t0"] for k in sets[c])
                det = sorted(gt[k]["t0"] for k in sets[c] & K)
                ttfd = (det[0] - occ[0]) if det and occ else float("nan")
                m[f"detected_any_{c}"] = (1.0 if det else 0.0, "bool")
                m[f"ttfd_{c}"] = (ttfd, "s after first occurrence")
                wins = defaultdict(lambda: [0, 0])
                for k in sets[c]:
                    wi = int((gt[k]["t0"] - load_t0) // WINDOW_S)
                    wins[wi][0] += 1
                    if k in K:
                        wins[wi][1] += 1
                wl = [v for v in wins.values() if v[0] > 0]
                m[f"window_detection_prob_{c}"] = (sum(1 for v in wl if v[1] > 0) / len(wl) if wl else float("nan"), f"fraction of {WINDOW_S}s windows with occurrence")
                for wi, (o, d) in sorted(wins.items()):
                    det_rows.append({"run_id": run, "config": cfg, "drift_class": c, "window_start_s": wi * WINDOW_S,
                                     "occurrences": o, "retained": d})
            st = stats.get(f"c2-{cfg}", [])
            if st:
                m["cpu_mean"] = (statistics.mean(x for x, _ in st) / 100, "cores")
                m["mem_mean"] = (statistics.mean(y for _, y in st), "MiB")
                m["mem_max"] = (max(y for _, y in st), "MiB")
            pr = prom(os.path.join(rd, f"metrics_{cfg}.prom"))
            m["receiver_accepted_spans"] = (psum(pr, "otelcol_receiver_accepted_spans"), "spans")
            m["receiver_refused_spans"] = (psum(pr, "otelcol_receiver_refused_spans"), "spans")
            m["memory_limiter_refused_spans"] = (psum(pr, "otelcol_processor_refused_spans", 'processor="memory_limiter"'), "spans")
            m["exporter_sent_spans"] = (psum(pr, "otelcol_exporter_sent_spans"), "spans")
            m["exporter_send_failed_spans"] = (psum(pr, "otelcol_exporter_send_failed_spans"), "spans")
            # (the OpenCensus measures are shared by tail_sampling/transparency/probabilistic_sampler: take one name)
            m["dropped_too_early"] = (max(psum(pr, f"otelcol_processor_{p}_sampling_trace_dropped_too_early") for p in ("transparency", "tail_sampling")), "traces")
            m["late_spans_dropped"] = (psum(pr, "otelcol_processor_transparency_tp_late_spans_dropped"), "spans")
            m["late_spans_forwarded"] = (psum(pr, "otelcol_processor_transparency_tp_late_spans_forwarded"), "spans")
            m["evicted_undecided"] = (psum(pr, "otelcol_processor_transparency_tp_traces_evicted_undecided"), "traces")
            q = hist_quantiles(pr, "otelcol_processor_transparency_tp_sampling_decision_latency_ns")
            m["decision_latency_p50"] = (q[0.5], "ns per policy evaluation")
            m["decision_latency_p99"] = (q[0.99], "ns per policy evaluation")
            for k, (v, u) in m.items():
                rows.append({"scenario": a.scenario, "run_id": run, "config": cfg, "metric": k, "value": v, "unit": u})
        print(f"{run}: {len(gt)} traces, {total_spans} spans, " + ", ".join(f"{c}={len(sets[c])}" for c in CLASSES), file=sys.stderr)
    def dump(name, rr, fields):
        with open(os.path.join(a.out, name), "w", newline="") as f:
            w = csv.DictWriter(f, fieldnames=fields); w.writeheader(); w.writerows(rr)
    dump("metrics_long.csv", rows, ["scenario", "run_id", "config", "metric", "value", "unit"])
    dump("detection_windows_long.csv", det_rows, ["run_id", "config", "drift_class", "window_start_s", "occurrences", "retained"])
    dump("docker_stats_long.csv", res_rows, ["unix_time_s", "run_id", "container", "cpu_percent", "mem_mib"])
    by, units = defaultdict(list), {}
    for r in rows:
        if r["value"] == r["value"]:
            by[(r["config"], r["metric"])].append(r["value"]); units[r["metric"]] = r["unit"]
    summ = defaultdict(dict)
    for (cfg, met), xs in by.items():
        lo, hi = ci95(xs)
        summ[cfg][met] = {"mean": statistics.mean(xs), "sd": statistics.stdev(xs) if len(xs) > 1 else 0.0, "n": len(xs),
                          "min": min(xs), "max": max(xs), "ci95_low": lo, "ci95_high": hi, "unit": units[met]}
    json.dump({"scenario": a.scenario, "runs": a.runs, "configs": summ}, open(os.path.join(a.out, "summary.json"), "w"), indent=1)


if __name__ == "__main__":
    main()
