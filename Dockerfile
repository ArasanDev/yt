# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/ArasanDev/yt/internal/version.Version=${VERSION} \
      -X github.com/ArasanDev/yt/internal/version.Commit=${COMMIT} \
      -X github.com/ArasanDev/yt/internal/version.Date=${DATE}" \
    -o /out/yt ./cmd/yt

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="yt"
LABEL org.opencontainers.image.description="YouTube transcripts, search, and channel monitoring for agents and humans"
LABEL org.opencontainers.image.source="https://github.com/ArasanDev/yt"
LABEL org.opencontainers.image.licenses="MIT"
LABEL io.modelcontextprotocol.server.name="io.github.ArasanDev/yt"

COPY --from=build /out/yt /usr/local/bin/yt

USER nonroot:nonroot

# With no TTY, bare `yt` serves MCP over stdio, which is the useful default in
# a container. Override the command for CLI use:
#   docker run --rm ghcr.io/arasanDev/yt transcript <url>
ENTRYPOINT ["/usr/local/bin/yt"]
CMD ["mcp"]
