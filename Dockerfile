# syntax=docker/dockerfile:1
FROM golang:1.26-bookworm AS source
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOPROXY=off GOSUMDB=off

FROM source AS test
RUN test "$(go list -m all)" = github.com/sky87/skytools \
    && test -z "$(gofmt -l cmd internal)" && go vet ./... && go test -count=1 ./...

FROM test AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
RUN GOOS=$TARGETOS GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/skytools ./cmd/skytools

FROM scratch AS binary
COPY --from=build /out/skytools /skytools

# Minimal execution image: no Go, Node, Rust, Java, or developer toolchains.
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates curl git unzip xz-utils libicu72 libssl3 libstdc++6 tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 dev
COPY --from=build /out/skytools /usr/local/bin/skytools
RUN ln -s skytools /usr/local/bin/sky-tools
USER dev
ENV HOME=/home/dev
WORKDIR /home/dev
ENTRYPOINT ["skytools"]
CMD []

# OCaml builds its compiler from source. Keep its native prerequisites out of
# the minimal runtime, and opt into Docker as opam's isolation boundary.
FROM runtime AS full
USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential m4 pkg-config libgmp-dev bubblewrap rsync \
    && rm -rf /var/lib/apt/lists/*
USER dev
ENV SKY_TOOLS_DISABLE_OPAM_SANDBOX=1

FROM runtime AS smoke
COPY scripts/smoke.sh /tmp/smoke.sh
RUN bash /tmp/smoke.sh
