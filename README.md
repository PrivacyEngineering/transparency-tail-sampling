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


