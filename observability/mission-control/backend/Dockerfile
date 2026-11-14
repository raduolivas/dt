# syntax=docker/dockerfile:1
# Build context: the repository root (Mission Control imports backend/pkg and backend/gen through
# replace directives). Multi-stage: web build, Go build, then a static non-root runtime that serves
# the built UI at "/" (ruling R3).
FROM node:24-trixie-slim AS web
RUN npm install -g pnpm@12.14.0
WORKDIR /web
COPY observability/mission-control/web/package.json observability/mission-control/web/pnpm-lock.yaml observability/mission-control/web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY observability/mission-control/web/ ./
RUN pnpm build

FROM golang:1.27.4-trixie AS build
WORKDIR /src
COPY backend/pkg ./backend/pkg
COPY backend/gen ./backend/gen
COPY observability/mission-control/backend ./observability/mission-control/backend
WORKDIR /src/observability/mission-control/backend
RUN GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mc ./cmd/mc

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mc /mc
COPY --from=web /web/dist /web
USER nonroot:nonroot
ENTRYPOINT ["/mc"]
