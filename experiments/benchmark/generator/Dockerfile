FROM golang:1.20-alpine AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /tiltgen . && go clean -cache -modcache
FROM scratch
COPY --from=build /tiltgen /tiltgen
ENTRYPOINT ["/tiltgen"]
