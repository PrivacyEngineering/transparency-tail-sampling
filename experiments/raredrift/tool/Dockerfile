# Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
FROM golang:1.20-alpine AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /rdtool . && go clean -cache -modcache
FROM scratch
COPY --from=build /rdtool /rdtool
ENTRYPOINT ["/rdtool"]
