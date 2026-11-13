# transparency-tail-sampling

**Transparency-aware tail sampling (TATS)** for OpenTelemetry: a Collector processor that checks span annotations
against a machine-readable [TILT](https://github.com/Transparency-Information-Language) transparency declaration and
uses tail-based sampling to retain the complete traces of non-conforming requests as evidence.

Paper: *Transparency-Aware Observability: Tail-Based Tracing of Personal Data against Machine-Readable Declarations
in Microservices.* Preprint: `<PREPRINT LINK>`

The implementation builds on the TU Berlin master's thesis of Bengisu Akkir, supervised by Elias Grünewald.

Contents:

| Path | What |
|---|---|
| `collector/` | Custom OpenTelemetry Collector distribution with the **transparency processor**, imported with history as a git subtree of [`akkbng/otel-transparency-collector`](https://github.com/akkbng/otel-transparency-collector) @ `8ff3c68`, plus build fixes, the `tilt_conformity` policy, composite-policy options, and tests (see [Changes](#changes-to-the-original-code)) |
| `deploy/` | docker-compose deployment: the TILT-instrumented Astronomy Shop (resource-trimmed), the transparency collector, Tempo, Prometheus, Grafana (transparency dashboard) |
| `tilt/` | `tilt.thesis.json` (original TILT document, unchanged) and `tilt.corrected.json` (built-in drift removed, used as the clean baseline in the experiments) |
| `instrumentation/` | Patch with the manual TILT instrumentation of 5 services, and pinned image digests (`IMAGES.txt`) |
| `reference/thesis-infrastructure/` | Terraform files and Helm values of the original GKE deployment, for reference |
| `experiments/` | Completeness experiments (v1, v2), performance benchmark, and rare-drift sweep: scripts, configs, README. Outputs are written to `experiments/results/` and `experiments/raw/` (not versioned) |

## Provenance and licence

* `collector/` derives from `akkbng/otel-transparency-collector` (Apache-2.0). That project is in turn a modified copy
  of the OpenTelemetry Collector contrib `processor/tailsamplingprocessor` and of contrib `internal/*` packages
  (v0.80.0, © The OpenTelemetry Authors, Apache-2.0). Upstream history is kept (subtree import commit `711dc6d`).
  The licence is in `collector/LICENSE`.
* `instrumentation/astronomy-shop-tilt-instrumentation.patch` = `git diff 8653756 0142455` (5 service source files)
  of [`akkbng/opentelemetry-astronomy-shop`](https://github.com/akkbng/opentelemetry-astronomy-shop), a fork of
  `open-telemetry/opentelemetry-demo` (Apache-2.0). The services run from the fork's original CI images
  (`ghcr.io/akkbng/opentelemetry-astronomy-shop:<service>`, pinned by digest in `deploy/docker-compose.yml`, built
  2023-05/06). `tilt/tilt.thesis.json` is the fork's `tilt.json` @ `0142455`.
* `deploy/grafana/dashboards/transparency-dashboard.json` and `reference/thesis-infrastructure/*` come from
  [`akkbng/thesis-infrastructure`](https://github.com/akkbng/thesis-infrastructure) @ `bd7037b`.
* All three upstream repos were verified to be identical to the code snapshot submitted with the thesis.
* New files in this repository: Apache-2.0.

## Requirements

Docker (tested: Docker Desktop 29.x on macOS arm64, VM with 12 CPUs / 7.7 GiB), about 4 GB of free disk for images,
`python3` (+ `pip install zstandard` for the analyses), `curl`. The shop images are linux/amd64 only. On arm64 they run
under emulation.

## 1. Build the collector

```sh
docker build -t otel-transparency-collector:dev collector
# unit tests (thesis policy + hardened policy + composite/processor hardening):
docker run --rm -v "$PWD/collector":/src -w /src golang:1.20-alpine \
  sh -c 'CGO_ENABLED=0 go test . ./internal/sampling/'
```

## 2. Run the demo (shop + transparency collector + Tempo/Prometheus/Grafana)

```sh
cd deploy
docker compose --profile demo up -d        # ~3.5 GB RAM
# Grafana http://localhost:3000 (dashboard "Transparency"), Prometheus :9090, Tempo :3200,
# shop http://localhost:8080, Locust http://localhost:8089 (10 users, autostart)
```

Checks:

```sh
curl -s localhost:8888/metrics | grep transparency_count_traces_sampled     # processor samples
curl -sG localhost:3200/api/search --data-urlencode 'q={span.tilt.check_flag=false}' | head -c 400
curl -s localhost:9090/api/v1/query --data-urlencode \
  'query=traces_service_graph_request_total{server_tilt_check_flag!=""}'   # service graph with tilt dimensions
python3 experiments/tempo_trace_dump.py < <(curl -s localhost:3200/api/traces/<trace id>)
```

The demo collector uses the thesis configuration (Listing 5.4 + 5.5), `deploy/otelcol/otelcol-transparency.yaml`.
It uses the original TILT (`TILT_FILE` in `deploy/.env`), which contains drift that is built into the demo (email
and cart services declare no category; shipping annotates an undeclared "tracking id").

Trimmed compared to the thesis deployment (none of these processes personal data or is TILT-instrumented):
accounting and fraud-detection services (Kafka consumers), the Envoy frontend proxy, Jaeger, and featureflag +
Postgres (replaced by a stub that returns "flag off": `deploy/featureflag-stub/`). Kafka is
`apache/kafka:3.9.1` (KRaft, native arm64) instead of the fork's instrumented Kafka image, so there are no
Kafka-broker spans.

Stop with `docker compose --profile demo down`.

## 3. Experiments

See `experiments/README.md` for exact commands, versions and hardware.

* **Completeness v1** (`experiments/run_experiment.sh`): head 1 %/10 %, contrib
  tail sampling, thesis transparency (composite and single) under identical Locust load with injected drift.
* **Completeness v2** (`experiments/v2/`): ground truth tapped before
  `memory_limiter`; drift classes V1/V2/V3 injected and labelled per trace; adds the hardened policy; detection
  probability and time-to-first-detection; losses per stage.
* **Benchmark** (`experiments/benchmark/`): one collector at a time
  (2 CPUs, 2 GiB) under a synthetic OTLP load sweep. It compares the baseline, contrib tail sampling, the thesis
  transparency config and the hardened config, each with the check on and off.
* **Rare-drift sweep** (`experiments/raredrift/`): synthetic load at 5,000 spans/s with drift injected at rates from
  1 in 10 to 1 in 10,000 traces; recall, detection probability per 60 s window, and time to first detection for head
  sampling, tail sampling baselines, and TATS.

## Changes to the original code

`FIX-n` make the original code build and run today without changing its behaviour; `ADD-n` add components and metrics
for the experiments; `H-n` are opt-in extensions (a new policy type or a new switch). With default settings, the binary
reproduces the original behaviour.

| ID | Change |
|---|---|
| FIX-1 | Dockerfile: pin the Collector Builder to v0.80.0 (the version of all components) and build with Go 1.20 |
| FIX-2 | `builder.yaml`: path of the Go binary inside the build image |
| FIX-3 | Dockerfile: build for the target platform (arm64 and amd64) instead of hard-coded amd64 |
| FIX-4 | `builder.yaml`: semver module version for the local processor module |
| FIX-5 | Configurable TILT location (`tilt_url`, `http(s)://` or `file://`) |
| FIX-6 | TILT fetch and decode errors are reported instead of panicking; TILT state is kept per policy instance |
| FIX-7 | Dockerfile: clean the Go caches in the build layer |
| ADD-1 | Contrib tail-sampling and probabilistic processors are part of the distribution |
| ADD-2 | `transparency_attribute.ignore_unannotated_spans` (default false) |
| ADD-3 | `disabled` switch for both TILT policies (check off) |
| ADD-4/5 | Decision-latency histogram and counters for late spans and undecided evictions |
| ADD-6 | Unit tests for both TILT policies, rate allocation, and processor options |
| H-1 | `tilt_conformity`: exact category match after splitting at semicolons; union over all TILT entries of a service |
| H-2 | `tilt_conformity`: annotated spans of services missing from the TILT document are flagged (V3) |
| H-3 | `tilt_conformity`: TILT pinned by SHA-256 (`tilt_sha256`), periodic refresh (`refresh_interval`), last verified copy kept, fail-closed |
| H-4 | `tilt_conformity`: all spans are checked (no early exit); reasons selectable via `sample_on` |
| H-5 | `tilt_conformity`: flags are written while holding the trace lock |
| H-6 | Processor option `late_span_check`: late spans are checked; violating ones are forwarded |
| H-7 | Composite options: `fall_through`, `unallocated_share_remaining`, `enforce_total_budget`, `unbudgeted_policies` |
| H-8 | Processor option `decide_on_eviction`: traces evicted at the buffer limit are decided on their partial content |
