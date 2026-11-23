# Agent image. The Laya classifier sidecar is built from Dockerfile.laya.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/validator-update-agent ./cmd/agent

# Runs as root (not :nonroot) to access the mounted, root-owned docker.sock.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/validator-update-agent /validator-update-agent
ENTRYPOINT ["/validator-update-agent"]
