# Multi-stage build of the otel-transparency-collector distribution with the
# OpenTelemetry Collector Builder (ocb). See ../FIXES.md for why each line
# differs from the original thesis Dockerfile.
FROM golang:1.20-alpine AS builder

# FIX-1: pin ocb to the collector version used in builder.yaml (v0.80.0);
# `builder@latest` now requires a newer Go toolchain and a different manifest schema.
RUN go install go.opentelemetry.io/collector/cmd/builder@v0.80.0

WORKDIR /build
COPY . .

# FIX-3: build for the platform of the image being built (was hard-coded amd64).
ARG TARGETOS=linux
ARG TARGETARCH
ENV CGO_ENABLED=0
# (Go module/build caches are removed in the same layer to keep the build cache small.)
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} builder --config builder.yaml && go clean -cache -modcache

FROM alpine:3.18 AS certs
RUN apk --no-cache add ca-certificates

FROM scratch
COPY --from=builder ["/tmp/otelcol/otel-transparency-collector", "/otelcol"]
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENTRYPOINT ["/otelcol"]
