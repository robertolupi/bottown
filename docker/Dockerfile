# syntax=docker/dockerfile:1
# bottown: Forgejo plus the bottown server in one image, supervised by the
# s6 that the rootful Forgejo image already runs. Build context: repo root.

ARG FORGEJO_VERSION=15.0.11

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/robertolupi/bottown/internal/cli.Version=${VERSION}" \
      -o /out/bottown ./cmd/bottown

FROM codeberg.org/forgejo/forgejo:${FORGEJO_VERSION}
ARG VERSION=dev
LABEL org.opencontainers.image.title="bottown" org.opencontainers.image.version="${VERSION}"

# Git over HTTP only, through bottown's proxy; no sshd.
RUN rm -rf /etc/s6/openssh
COPY docker/s6/bottown /etc/s6/bottown
COPY --from=build /out/bottown /usr/local/bin/bottown

# Forgejo settings applied by environment-to-ini on every start.
# ROOT_URL is per instance and set by `bottown up`.
ENV FORGEJO__security__INSTALL_LOCK=true \
    FORGEJO__database__DB_TYPE=sqlite3 \
    FORGEJO__database__PATH=/data/gitea/gitea.db \
    FORGEJO__service__DISABLE_REGISTRATION=true \
    FORGEJO__service__REQUIRE_SIGNIN_VIEW=true \
    FORGEJO__service__DEFAULT_KEEP_EMAIL_PRIVATE=true \
    FORGEJO__server__DISABLE_SSH=true \
    FORGEJO__server__START_SSH_SERVER=false \
    FORGEJO__server__OFFLINE_MODE=true \
    FORGEJO__actions__ENABLED=false \
    FORGEJO__mailer__ENABLED=false \
    FORGEJO__repository__DEFAULT_PRIVATE=private \
    BOTTOWN_DATA=/data/bottown \
    BOTTOWN_LISTEN=:8080 \
    BOTTOWN_FORGEJO_URL=http://127.0.0.1:3000

EXPOSE 3000 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
