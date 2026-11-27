# Runtime only. The deploy workflow cross-compiles the binaries on the GitHub
# runner and ships them beside this file, so the 2-CPU server never runs a Go
# build and the image holds the binaries and the migrations, nothing else.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget tzdata && \
    adduser -D -H -s /sbin/nologin relay && \
    mkdir -p /var/lib/relay && chown relay:relay /var/lib/relay
WORKDIR /opt/relay
COPY bin/control-api bin/seed-demo bin/operator-admin ./
COPY db ./db
USER relay
EXPOSE 8080
ENTRYPOINT ["/opt/relay/control-api"]
