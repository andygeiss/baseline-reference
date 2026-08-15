# syntax=docker/dockerfile:1
#
# One static binary in a minimal image — the same deliverable the baseline
# mandates, just shipped in a container instead of by scp.
# See operations/containers.md in the baseline for why containers are allowed.
#
# The build stage runs on the BUILD platform and cross-compiles to the TARGET
# platform. That keeps arm64 images buildable on an amd64 CI runner at native
# speed: no QEMU, because no arm64 instruction ever executes here.

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

# git is not a build dependency of the code — the toolchain shells out to it to
# read VCS metadata, which is what stamps info.Main.Version (Go 1.24+). Without
# git in PATH the binary reports "unknown" instead of its tag.
RUN apk add --no-cache git

WORKDIR /src

# Dependencies resolve in their own layer, so a code-only change does not
# re-download the module graph.
COPY go.mod go.sum ./
RUN go mod download

# The whole tree, .git included: see the note above.
COPY . .

ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -o /out/server ./cmd/server

# The state directory is built here, with its final ownership, so the runtime
# stage needs no RUN at all — see the note on the next stage.
RUN mkdir -p /out/state


FROM alpine:3.24

# This stage is pure COPY/metadata on purpose: a RUN here would execute arm64
# instructions, forcing QEMU emulation on an amd64 CI runner. With no RUN, the
# arm64 image assembles at native speed and needs no QEMU setup step.
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build --chown=10001:10001 /out/state /var/lib/app

# HOST=0.0.0.0 binds every interface *inside the container's network
# namespace*, which is not the same as being public: the compose file publishes
# the port to 127.0.0.1 only, so the proxy is still the sole public listener.
# OPS_PORT is deliberately never published — localhost-only is its access
# control, and inside the container that is exactly what it stays.
ENV HOST=0.0.0.0 \
    PORT=8080 \
    OPS_PORT=6060 \
    DATABASE_URL=/var/lib/app/app.db \
    ENV=prod \
    LOG_LEVEL=info

# Declared so a named volume mounted here inherits this directory's ownership
# on first use; without it the mount lands root-owned and the app cannot write.
VOLUME ["/var/lib/app"]

EXPOSE 8080

# Numeric UID, so no /etc/passwd entry is needed and no RUN adduser either.
# Nothing in this image needs root, and both ports are above 1024.
USER 10001:10001

# Reuses the ops listener the baseline already requires. busybox wget ships with
# alpine, so this adds no packages.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:6060/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/server"]
