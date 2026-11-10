# skyledger: one image, roles as subcommands (skyledger migrate|ingest|history|demo-feed).
# Runs on the build machine and cross-compiles, so the arm64 image needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine3.24 AS build
WORKDIR /src
# Dependencies first, so code changes don't download them again.
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /skyledger ./cmd/skyledger

# Alpine rather than an empty image: the health checks run `sh -c 'find ...'`.
FROM alpine:3.24
RUN adduser -S -u 10001 -H skyledger
COPY --from=build /skyledger /usr/local/bin/skyledger
USER 10001
ENTRYPOINT ["skyledger"]
CMD ["--help"]
