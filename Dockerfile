# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies change far less often than sources, so cache them separately.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/domain-lens-mcp ./cmd/domain-lens-mcp

# distroless/static ships CA certificates, which RDAP over HTTPS needs.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/domain-lens-mcp /usr/local/bin/domain-lens-mcp

# stdio is the default so `docker run -i` works as an MCP server out of the box.
ENV TRANSPORT=stdio HTTP_ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/domain-lens-mcp"]
