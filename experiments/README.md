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

<!-- BENCHMARK -->
