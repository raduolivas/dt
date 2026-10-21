# syntax=docker/dockerfile:1
# Build context: backend/. One image per service: --build-arg SERVICE=<name>.
FROM golang:1.27.3-trixie AS build
ARG SERVICE
WORKDIR /src
COPY go.work* ./
COPY pkg ./pkg
COPY gen ./gen
COPY services ./services
COPY tools ./tools
RUN test -n "${SERVICE}" && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./services/${SERVICE}/cmd/${SERVICE}

# Static, shell-less, non-root runtime (spec §11.1).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
