# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/doketfire ./cmd/doketfire

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S doketfire \
    && adduser -S -G doketfire -h /data doketfire \
    && mkdir -p /data \
    && chown doketfire:doketfire /data

COPY --from=build /out/doketfire /usr/local/bin/doketfire

USER doketfire
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8090/api/health >/dev/null || exit 1

ENTRYPOINT ["doketfire"]
CMD ["serve", "--dir=/data", "--http=0.0.0.0:8090"]
