# SPDX-FileCopyrightText: 2026 Setunari Group LLC
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Setunari build of Chatto: frontend + server from this source tree, packaged like the
# official image (docker/Dockerfile.goreleaser). Build from the repository root:
#   docker build -f setunari/Dockerfile --build-arg VERSION=0.5.0-setunari.1 -t setunari-chatto:dev .

ARG NODE_IMAGE=node:24.22.1-bookworm-slim
ARG GO_IMAGE=golang:1.27.3-alpine

FROM ${NODE_IMAGE} AS frontend
ARG VERSION=0.0.0-setunari
ENV CHATTO_BUILD_VERSION=${VERSION} \
    CHATTO_FRONTEND_PRECOMPRESS=1 \
    CI=1
WORKDIR /src
RUN corepack enable
COPY . .
RUN pnpm install --frozen-lockfile \
 && pnpm run build:frontend \
 && bash tools/prepare-embedded-frontend.sh apps/frontend/build cli/internal/http_server/.client

FROM ${GO_IMAGE} AS server
ARG VERSION=0.0.0-setunari
ARG TARGETARCH
WORKDIR /src
COPY --from=frontend /src /src
RUN cp LICENSES/AGPL-3.0-or-later.txt cli/LICENSE \
 && cp NOTICE cli/NOTICE \
 && cp LICENSES/AGPL-3.0-or-later.txt cli/cmd/embedded/LICENSE \
 && cp NOTICE cli/cmd/embedded/NOTICE \
 && cd cli \
 && CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -trimpath -tags=nomsgpack \
      -ldflags="-s -w -X main.Version=${VERSION}" -o /out/chatto .

FROM alpine:3.22
ARG NATS_CLI_VERSION=0.4.0
ARG TARGETARCH
ARG CHATTO_UID=1000
ARG CHATTO_GID=1000

ENV HOME=/home/chatto

# Runtime dependencies:
# - ffmpeg is used for media processing.
# - shadow provides usermod/groupmod for PUID/PGID remapping at startup.
# - su-exec drops from the root entrypoint to the unprivileged chatto user.
RUN set -eux; \
 apk add --no-cache \
      ca-certificates \
      ffmpeg \
      shadow \
      su-exec \
      tzdata

# Bundle the NATS CLI so operators can inspect the same NATS server Chatto uses
# with `docker exec <container> nats ...`. A wrapper maps Chatto's connection
# environment to the CLI without writing runtime configuration files.
RUN set -eux; \
 apk add --no-cache --virtual .build-deps curl unzip; \
 case "$TARGETARCH" in \
      amd64) arch=amd64 ;; \
      arm64) arch=arm64 ;; \
      *) echo "unsupported arch: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
 archive="nats-${NATS_CLI_VERSION}-linux-${arch}.zip"; \
 curl -fsSL -o "/tmp/${archive}" \
      "https://github.com/nats-io/natscli/releases/download/v${NATS_CLI_VERSION}/${archive}"; \
 curl -fsSL -o /tmp/nats-SHA256SUMS \
      "https://github.com/nats-io/natscli/releases/download/v${NATS_CLI_VERSION}/SHA256SUMS"; \
 cd /tmp; \
 grep "  ${archive}$" nats-SHA256SUMS | sha256sum -c -; \
 mkdir -p /usr/local/libexec; \
 unzip -j "/tmp/${archive}" "*/nats" -d /usr/local/libexec/; \
 chmod +x /usr/local/libexec/nats; \
 rm "/tmp/${archive}" /tmp/nats-SHA256SUMS; \
 apk del .build-deps

# Create the default runtime user and writable paths. PUID/PGID can remap this
# user at container startup; mounted directories are not recursively chowned.
RUN set -eux; \
 addgroup -g "$CHATTO_GID" chatto; \
 adduser -D -u "$CHATTO_UID" -G chatto -h "$HOME" -s /sbin/nologin chatto; \
 mkdir -p /config /data; \
 chown chatto:chatto /config /data "$HOME"

# GoReleaser places the compiled Linux binary in the Docker build context as
# `chatto`; the release image only packages that binary and the entrypoint.
COPY --from=server /out/chatto /chatto
COPY docker/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY docker/nats-wrapper.sh /usr/local/bin/nats
COPY LICENSES/AGPL-3.0-or-later.txt /usr/share/doc/chatto/LICENSE
COPY NOTICE /usr/share/doc/chatto/NOTICE
RUN chmod +x /chatto /usr/local/bin/docker-entrypoint.sh /usr/local/bin/nats

# Check the real binary in the runtime image before publishing it. A missing
# dynamic loader can prevent startup even when the binary is executable.
RUN /chatto --version

WORKDIR /
EXPOSE 4000

# The entrypoint handles optional PUID/PGID remapping, then execs
# `/chatto start -c /config/chatto.toml` by default.
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["start", "-c", "/config/chatto.toml"]
