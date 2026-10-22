# Experiments

All experiments are **laptop-scale**: one machine, Docker Desktop, emulated amd64 service images. They measure
*completeness* (which traces each sampler keeps) on real shop traffic, and *cost* on a controlled synthetic load.
Absolute CPU/memory numbers are not comparable to the thesis' GKE numbers (Table 6.1). Relative comparisons
within one run are meaningful, because every configuration sees the identical span stream.

Hardware/software: `results/host_info.txt` (Apple M4 Pro, 12 logical CPUs, 24 GiB; Docker Desktop 29.7.2, VM with 12 CPUs
and 7.7 GiB, cgroup v2; macOS 27.0.1). Collector under test: `otel-transparency-collector`, built from `collector/`
(ocb v0.80.0, Go 1.20, linux/arm64). The same binary is used for every sampling configuration. Fan-out and
sink: `otel/opentelemetry-collector-contrib:0.108.0` (not measured). Shop images: `instrumentation/IMAGES.txt`.

Python analysis: `python3 -m venv .venv && .venv/bin/pip install zstandard`.

Output conventions: tidy long CSV (`run_id, config, metric, value, unit`), plus `summary.json` (mean, sd, n, min,
max, 95 % CI using Student t). `ci95_*` is NaN for n < 2. Fractions are in [0,1], CPU in cores (1.0 = one core),
memory in MiB.

---------------------------------------------------------------------------------------------------------------------

## Completeness experiment v1 (`run_experiment.sh`, results in `results/v1/`)

**Setup.** The shop (`deploy/`) sends all telemetry to a fan-out collector (`configs/fanout.yaml`). The fan-out
forwards an identical copy of every span to 9 sampler collectors (`configs/sampler-*.yaml`, one container each).
Each sampler exports to a sink that writes each output to a zstd-compressed OTLP-JSON file. The ground truth is
the output of the non-sampling collector `none`. Load: Locust from the fork's load-generator image, 100 users,
spawn rate 10/s, 300 s per run (thesis: 100 users, 10/s, 5 min). There are 5 runs. Within a run all configs see
the same traffic.

**TILT.** `tilt/tilt.corrected.json`: the thesis TILT with its built-in drift removed (cartservice `user id`,
emailservice `customer email`, shippingservice adds `tracking id`).

**Drift injection** (in the fan-out, before every sampler): in traces whose id starts with hex `0` or `1` (1/8),
annotated paymentservice spans report the undeclared category `health data` (marked
`experiment.drift_injected=true`).

**Configs:** `none`; `head1`/`head10` (trace-id-hash probabilistic sampler, equivalent in outcome to head-based
TraceIdRatio sampling); `tailprob` (contrib tail sampling, probabilistic at `TAILPROB_PCT=31` %, set from a 2-min
pilot; see the caveat in the results); `tailpd` (contrib, OTTL "has tilt category", no limit); `tailthesis`
(contrib, thesis Listing 6.1); `transpthesis` (transparency processor, thesis Listing 5.4); `transp`
(transparency_attribute alone); `transpstrict` (transparency_attribute with ADD-2 `ignore_unannotated_spans`).

**Ground-truth classes per trace** (`analyze.py`): `pd` = some span has `tilt.dataDisclosed.category`; `viol_code` =
some span fails the check exactly as implemented (substring, first TILT entry, missing annotation ⇒ fail);
`viol_strict` = some *annotated* span fails; `drift` = injected.

```sh
cd deploy && OTEL_COLLECTOR_HOST=fanout docker compose up -d && cd ..     # shop without demo collector
for r in 1 2 3 4 5; do TAILPROB_PCT=31 experiments/run_experiment.sh run$r 300 100 10; done
.venv/bin/python experiments/analyze.py --raw experiments/raw --out experiments/results/v1 run1 run2 run3 run4 run5
```

**Results v1** (mean ± sd over runs; `results/v1/metrics_long.csv`, `summary.json`, per-trace ground truth in
`results/v1/traces/`, resources in `docker_stats_long.csv`):

n = 5 runs × 300 s; per run (mean ± sd) 15281 ± 257 traces, 74531 ± 2119 spans (≈248 spans/s), 2499 PD traces, 2499 thesis-semantics violations, 54.4 ± 8.9 drift traces.

| config | trace retention | span retention | recall PD traces | recall thesis-semantics violations | recall injected drift | share of kept traces with drift | CPU (mcores) | mem (MiB) |
|---|---|---|---|---|---|---|---|---|
| `none` | 1.000 ± 0.000 | 1.000 ± 0.000 | 1.000 ± 0.000 | 1.000 ± 0.000 | 1.000 ± 0.000 | 0.004 ± 0.001 | 5.2 ± 0.9 | 43.9 ± 6.6 |
| `head1` | 0.010 ± 0.001 | 0.011 ± 0.001 | 0.011 ± 0.001 | 0.011 ± 0.001 | 0.012 ± 0.017 | 0.004 ± 0.006 | 4.2 ± 1.0 | 42.5 ± 0.6 |
| `head10` | 0.100 ± 0.002 | 0.102 ± 0.005 | 0.101 ± 0.005 | 0.101 ± 0.005 | 0.124 ± 0.028 | 0.004 ± 0.001 | 4.5 ± 1.1 | 43.2 ± 1.5 |
| `tailprob` | 0.311 ± 0.003 | 0.311 ± 0.008 | 0.311 ± 0.011 | 0.311 ± 0.011 | 0.320 ± 0.094 | 0.004 ± 0.001 | 5.0 ± 1.2 | 62.6 ± 0.7 |
| `tailpd` | 0.163 ± 0.004 | 0.442 ± 0.011 | 1.000 ± 0.000 | 1.000 ± 0.000 | 1.000 ± 0.000 | 0.022 ± 0.003 | 6.0 ± 1.4 | 62.4 ± 1.2 |
| `tailthesis` | 0.163 ± 0.004 | 0.442 ± 0.011 | 1.000 ± 0.000 | 1.000 ± 0.000 | 1.000 ± 0.000 | 0.022 ± 0.003 | 6.5 ± 1.8 | 63.2 ± 1.8 |
| `transpthesis` | 0.163 ± 0.003 | 0.441 ± 0.011 | 0.999 ± 0.001 | 0.999 ± 0.001 | 1.000 ± 0.000 | 0.022 ± 0.003 | 6.1 ± 1.3 | 62.8 ± 0.7 |
| `transp` | 0.163 ± 0.004 | 0.442 ± 0.011 | 1.000 ± 0.000 | 1.000 ± 0.000 | 1.000 ± 0.000 | 0.022 ± 0.003 | 6.0 ± 1.4 | 62.5 ± 0.9 |
| `transpstrict` | 0.004 ± 0.001 | 0.031 ± 0.005 | 0.022 ± 0.003 | 0.022 ± 0.003 | 1.000 ± 0.000 | 1.000 ± 0.000 | 4.9 ± 1.3 | 61.3 ± 1.4 |

Observations:
* With the corrected TILT, **every** personal-data trace is a violation under the thesis semantics
  (2 499 = 2 499 per run). Each one contains an unannotated span of a TILT-listed service (gRPC client spans,
  Redis, ...). So `transp` behaves exactly like the PD-presence policy `tailpd` (identical retained sets). Only the
  injected drift (54/run) is a real declaration mismatch.
* `transpthesis` (Listing 5.4) loses 0.1 % of the violating traces (rate limit, no fall-through) even at ~250 spans/s.
  `tailthesis` = `tailpd`, i.e. `always_sample` contributes nothing (0 budget).
* `head1`/`head10`/`tailprob` keep violations only in proportion to their sampling rate. Drift recall is
  0.012 ± 0.017, 0.124 ± 0.028 and 0.320 ± 0.094.
* `transpstrict` (ADD-2) keeps 0.4 % of traces (3.1 % of spans), and all of them are drift (precision 1.0, recall 1.0).
* `tailprob` was configured at 31 %, the retention of `transp` in a 2-min pilot right after collector start
  (dominated by Node `fs` start-up traces). In steady state `transp` keeps 16.3 %, so `tailprob` keeps ~2× more
  traces than intended and still has 0.31 recall. v2 uses 16 %.
* CPU (≈5 mcores) and memory (≈43 MiB without, ≈62 MiB with a 30 s tail buffer) are tiny at this load. Differences
  between configs are within noise. The decision-latency metric of v1 is the µs-truncated original
  (`policy_decision_latency_mean` in µs); v2 and the benchmark use the ns histogram.


---------------------------------------------------------------------------------------------------------------------

## Completeness experiment v2 (`v2/`, results in `results/v2/`)

This revision answers the internal review. It changes the following relative to v1:

* **Ground truth is tapped in the fan-out** (exported directly to the sink), i.e. *before* any `memory_limiter`.
  Each sampler pipeline is `memory_limiter → sampler → batch`, with a 300 MiB container limit and
  `limit_mib: 240`, `spike_limit_mib: 60`, as in the thesis Helm deployment.
* **Three labelled drift classes** are injected into disjoint trace subsets (first hex digit of the trace id;
  1/16 of the eligible traces each). Modified spans carry `experiment.drift_class`:
  * V1 *undeclared category*: paymentservice reports `health data`.
  * V2 *missing annotation*: paymentservice spans lose `tilt.dataDisclosed.category`.
  * V3 *unlisted service*: productcatalogservice (not in the TILT) server spans report `purchase history`.
* **Natural V2** (`nat_V2`) = un-injected spans of TILT-listed services without annotation (auto-instrumentation).
  `budget_share_nat_V2_noise` = share of retained spans that belong to traces whose *only* issue is natural V2.
* **TILT pinned** for the hardened policies (`tilt_sha256`).
* **Hardened variants:** `hardened` (tilt_conformity alone; V1/V3 sampled, V2 flagged only; late-span check;
  decide-on-eviction) and `hardenedcomp` (V1/V3 evidence unbudgeted, V2 25 %, PD presence 25 %, always_sample with
  the remaining 50 %, fall-through, global budget).
* **Detection metrics per injected class:** recall, detected-at-all, time to first detection (`ttfd_*`, seconds
  between the first occurrence and the first retained occurrence, by trace start time), and per-60-s-window
  detection probability (`detection_windows_long.csv` has the raw window counts).
* **Losses per stage** from the collectors' own metrics: memory_limiter refusals, `sampling_trace_dropped_too_early`,
  late spans dropped/forwarded, traces evicted before a decision, exporter send failures.

```sh
cd deploy && OTEL_COLLECTOR_HOST=fanout docker compose up -d && cd ..
for r in 1 2 3 4 5; do TAILPROB_PCT=16 experiments/v2/run_v2.sh run$r 300 100 10; done
.venv/bin/python experiments/v2/analyze_v2.py --raw experiments/raw/v2 --out experiments/results/v2 run1 run2 run3 run4 run5
```

<!-- V2 RESULTS -->

---------------------------------------------------------------------------------------------------------------------

## Performance benchmark (`benchmark/`, results in `results/benchmark/`)

**Setup.** Each collector configuration runs alone (nothing else on the Docker host: shop and earlier stacks stopped) in
`experiments/benchmark/docker-compose.benchmark.yml`: collector under test (**2 CPUs, 2 GiB**, same binary
`otel-transparency-collector:dev`, linux/arm64) → discarding sink (`contrib 0.108.0`, not measured); the TILT is served
locally by nginx (`tilt.corrected.json`; the hardened configs additionally pin its SHA-256); the synthetic generator
`tiltgen` (own container, 4 CPUs, Go, OTLP/gRPC) offers a constant load in spans/s. Traces mimic the shop: 50 % browse
(6 spans, no personal data), 30 % cart (5 spans, `user id`), 20 % checkout (7 spans, `customer email`, `Credit Card`,
`tracking id`), mean 5.9 spans/trace, `tilt.*` attributes on 1-3 spans per trace. 49 % of all spans belong to traces with
personal data ("matching" spans, `0.49 × load` spans/s). 10 % of checkout traces (2 % of traces) report the undeclared
category `health data` (drift). Configs (`benchmark/configs/`): **B0** pass-through, **B1** contrib `tail_sampling`
with the thesis base composite (Listing 6.1), **B2** transparency processor with the thesis composite (Listing 5.4),
**B3** hardened composite (`tilt_conformity`, fall-through, global budget, unbudgeted evidence, late-span check,
decide-on-eviction), **B2-off / B3-off** = B2 / B3 with the check disabled (`disabled: true`; identical processor, policies
and budget, the check policy always votes NotSampled), which isolates the cost of the check. All sampling configs:
`decision_wait 30 s`, `num_traces 50000`, `max_total_spans_per_second 1000`. Per cell: fresh container, 35 s warm-up
(discarded; longer than `decision_wait`), 45 s steady state. Rates come from counter deltas of the collector's own
Prometheus endpoint over the steady window, CPU/RSS from `docker stats` (~1 sample/s), decision latency from the delta of
the histogram `tp_sampling_decision_latency_ns` (B2/B3; interpolated within buckets) or the µs-truncated contrib histogram (B1,
coarse). Latency is per policy evaluation, not end-to-end.

**What was cut** relative to the plan (5 reps × ≥ 3 min per step, 2.5 h wall-clock): 45 s steady windows instead of
≥ 3 min; **n = 3** reps for 1 k, 2 k, 5 k, 10 k spans/s and **n = 1** for 500 and 20 k spans/s (no CI there); the whole
series (84 cells) took ≈ 2 h. Sweep stops at 20 k spans/s although the collector never reached CPU saturation (max 0.43 core
of 2); the limit found is the trace buffer (see below). Laptop scale: Apple M4 Pro, Docker Desktop VM, cgroup-limited
containers, generator and sink share the CPU; absolute numbers are not comparable with the thesis' GKE numbers.

```sh
docker build -t tiltgen:dev experiments/benchmark/generator        # once
docker image inspect otel-transparency-collector:dev >/dev/null     # built from collector/ (see Makefile/README)
experiments/benchmark/run_all.sh                                     # whole series, ~2 h, raw data in experiments/raw/benchmark/sweep/
#   = run_benchmark.sh 2 "<6 configs>" "1000 2000 5000 10000"; run_benchmark.sh 1 "<6 configs>" "500 20000"; then rep 3 of the main grid
#   single cell: experiments/benchmark/run_benchmark.sh REPS "B3-hardened" "2000" [WARMUP_S=35] [STEADY_S=45]
python3 experiments/benchmark/analyze_benchmark.py experiments/raw/benchmark/sweep experiments/results/benchmark
python3 experiments/benchmark/make_table.py experiments/results/benchmark
```

Outputs in `results/benchmark/`: `benchmark_long.csv` (config, offered_spans_per_s, rep, metric, value, unit),
`summary.json` (config → load → metric → mean, sd, n, ci95_low/high, unit), `benchmark_docker_stats_long.csv` (all
`docker stats` samples), `results_table.md` (tables incl. paired check cost), `host_info.txt`.

**Results** (full tables in `results/benchmark/results_table.md`):

* **CPU is negligible and not the limit.** All configs stay below 0.45 core (2-core limit): 0.03 core at 1 k, 0.06 at 2 k,
  0.12-0.13 at 5 k, 0.19-0.23 at 10 k, 0.34-0.43 at 20 k spans/s (B0 pass-through 0.03 / 0.05 / 0.10 / 0.16 / 0.25).
* **Memory is the buffered traces:** 30 s × load. B0 ≈ 50 MiB; B1/B2/B3 ≈ 113 / 189 / 417 / 760-776 MiB at 1 k / 2 k / 5 k / 10 k
  (sd ≤ 6 MiB). The buffer is full at `num_traces = 50000` (≈ 1 670 traces/s ≈ 9.8 k spans/s).
* **The saturation point is the trace buffer, not CPU.** Above ≈ 9.8 k spans/s `num_traces` is exceeded before the 30 s
  `decision_wait`: B1 and B2 export **0** spans at 10 k and 20 k (`sampling_trace_dropped_too_early` 1.69 k traces/s at
  10 k, 3.4 k at 20 k; B2 additionally `tp_traces_evicted_undecided` = the same, counted for the first time by our patch).
  B3 (`decide_on_eviction`) still exports 977 ± 2 spans/s at 10 k and 1 222 at 20 k, i.e. its budgeted output survives
  the overload, but it decides on partial traces. No receiver refusals or generator errors occurred at any load.
* **Budget cliff.** Matching spans/s = 0.49 × load; the budgets are 750 (B1 PD policy 75 %), 500 (B2 tilt-check 50 %) and
  250 (B2-off, only the PD policy 25 % is active) spans/s. Exported spans/s (n = 3): B1 488 ± 5 → 753 ± 19 → 758 ± 10 at
  1 k / 2 k / 5 k; B2 477 ± 7 → 498 ± 11 → 501 ± 7 (plateau at the 500 budget from ≈ 1 k, where 0.49 × load = 490 ≈ budget; ≥ 97 %
  of the matching spans are kept at 1 k, 51 % at 2 k, 20 % at 5 k); B2-off 251 → 252 → 254. The thesis variant therefore
  caps the evidence at its sub-policy budget; surplus non-conforming traces are dropped (no fall-through). B3 keeps the
  V1/V3 evidence outside the budget, so its output *grows with the amount of evidence*: 734 ± 11 → 790 ± 2 → 878 ± 5 → 977 ± 2 spans/s
  (1 k … 10 k) instead of plateauing; the budgeted classes (V2, PD presence) are capped at 25 % each (B3-off: 498 → 494 → 506 → 494
  flat). At 500 spans/s (n = 1) B3 exports 470 (B2 248).
* **Cost of the check.** Paired B3 − B3-off: CPU +3.9 mcores [2.2, 5.5] at 1 k, +2.7 [-9.6, 15.1] at 2 k, +2.5 [-3.5, 8.4] at 5 k,
  +14.5 [-19, 48] at 10 k (n = 3); B2 − B2-off: +2.3 [-1.8, 6.4] at 1 k, +8.0 [1.6, 14.3] at 5 k, ≈ 0 elsewhere. That is ≤ 10 % of the
  total CPU (≈ 3 % at 5 k) and mostly within noise. Per policy evaluation the median decision latency rises by ≈ 0.7-1.0 µs
  (B2, p50 3.7-4.5 µs vs 3.0-3.7) and ≈ 1.6-2.8 µs (B3, p50 5.0-6.1 µs vs 3.2-3.7 µs). p99 is 15-39 µs for all configs
  (B3 23-34 µs vs 18-22 µs for B3-off); the contrib B1 (µs-truncated histogram) has p50 ≈ 3.3-3.5 µs. B1, B2 and B3 differ by
  ≤ 20 % in total CPU (≤ 0.03 core) and ≤ 3 % in memory at every load. First-cell artefact: B0 at 1 k had one rep with a high RSS (74 ± 47 MiB).
* **Hardened `tilt_conformity`** is ≈ 2× more expensive per decision than the thesis check (it checks every span instead
  of stopping at the first failing span and uses exact set membership), but this is invisible in total CPU at laptop scale.
