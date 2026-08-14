# Multi-stage: the runtime image carries the binary and nothing else.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static build so the runtime image needs no libc at all.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/control-api ./cmd/control-api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/seed-demo ./cmd/seed-demo

FROM alpine:3.20
# ca-certificates for outbound https (webhooks); wget for the healthcheck.
RUN apk add --no-cache ca-certificates wget && \
    adduser -D -H -s /sbin/nologin relay
WORKDIR /opt/relay
COPY --from=build /out/control-api /out/seed-demo ./
COPY db ./db
USER relay
EXPOSE 8080
ENTRYPOINT ["/opt/relay/control-api"]
