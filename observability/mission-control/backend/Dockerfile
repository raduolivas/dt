# syntax=docker/dockerfile:1
# Build context: the repository root (Mission Control imports backend/pkg and backend/gen through
# replace directives). Multi-stage: Go build, then a static non-root runtime. A web build stage joins in
# the web task; until then there is no UI to serve.
FROM golang:1.27.3-trixie AS build
WORKDIR /src
COPY backend/pkg ./backend/pkg
COPY backend/gen ./backend/gen
COPY observability/mission-control/backend ./observability/mission-control/backend
WORKDIR /src/observability/mission-control/backend
RUN GOWORK=off CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mc ./cmd/mc

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mc /mc
USER nonroot:nonroot
ENTRYPOINT ["/mc"]
