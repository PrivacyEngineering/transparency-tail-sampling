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

<!-- V1 RESULTS -->

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
