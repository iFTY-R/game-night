# syntax=docker/dockerfile:1.8

ARG GO_VERSION=1.26.4
ARG NODE_VERSION=24.18.0
ARG PNPM_VERSION=11.13.1

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-bookworm-slim AS web-build
ARG PNPM_VERSION
WORKDIR /src
ENV CI=1 \
    PNPM_HOME=/pnpm \
    PATH=/pnpm:${PATH}

RUN corepack enable && corepack prepare pnpm@${PNPM_VERSION} --activate

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc .node-version ./
COPY apps/web/package.json apps/web/package.json
COPY games/dice-789/client/package.json games/dice-789/client/package.json
COPY games/dice-789/themes/package.json games/dice-789/themes/package.json
COPY games/liars-dice/client/package.json games/liars-dice/client/package.json
COPY games/liars-dice/themes/package.json games/liars-dice/themes/package.json
COPY games/meet-by-chance/client/package.json games/meet-by-chance/client/package.json
COPY games/meet-by-chance/themes/package.json games/meet-by-chance/themes/package.json
COPY packages/game-ui-kit/package.json packages/game-ui-kit/package.json
COPY packages/test-kit/package.json packages/test-kit/package.json
COPY packages/theme-system/package.json packages/theme-system/package.json
COPY sdk/ts/game-client/package.json sdk/ts/game-client/package.json
COPY tooling/workspace-smoke/package.json tooling/workspace-smoke/package.json

RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store \
    pnpm install --frozen-lockfile --store-dir /pnpm/store

COPY . .
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS go-build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
ENV CGO_ENABLED=0 \
    GOFLAGS=-buildvcs=false

COPY go.mod go.sum ./
RUN --mount=type=cache,id=gomodcache,target=/go/pkg/mod \
    --mount=type=cache,id=gobuildcache,target=/root/.cache/go-build \
    go mod download

COPY . .
RUN --mount=type=cache,id=gomodcache,target=/go/pkg/mod \
    --mount=type=cache,id=gobuildcache,target=/root/.cache/go-build \
    set -eu; \
    mkdir -p /out/bin; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/bin/game-night ./apps/launcher; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/bin/edge ./apps/edge; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/bin/api ./apps/api; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/bin/realtime ./apps/realtime; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/bin/worker ./apps/worker; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/bin/migrate ./apps/migrate

FROM debian:bookworm-slim AS runtime
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown

ENV DEBIAN_FRONTEND=noninteractive \
    TZ=Etc/UTC \
    PATH=/app/bin:${PATH}

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 --system game-night \
    && useradd --uid 10001 --gid 10001 --system --home-dir /nonexistent --shell /usr/sbin/nologin game-night \
    && mkdir -p /app/bin /app/web /app/infra/migrations \
    && chown -R 10001:10001 /app

WORKDIR /app

COPY --from=go-build --chown=10001:10001 /out/bin/ /app/bin/
COPY --from=web-build --chown=10001:10001 /src/apps/web/dist/ /app/web/
COPY --from=go-build --chown=10001:10001 /src/infra/migrations/ /app/infra/migrations/

LABEL org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.created=$CREATED

USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["game-night"]
CMD ["serve-all"]
