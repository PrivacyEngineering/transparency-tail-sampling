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
