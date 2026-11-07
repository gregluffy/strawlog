# syntax=docker/dockerfile:1
# ---- UI ----
FROM node:22-alpine AS ui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build            # -> /internal/hub/dist

# ---- Go ----
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
COPY --from=ui /internal/hub/dist ./internal/hub/dist
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/strawlog ./cmd/strawlog

# ---- runtime ----
# Debian slim only because journalctl is needed to read binary journald files.
# ca-certificates: outbound HTTPS (Discord/Teams/webhook notifications).
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends systemd ca-certificates \
 && rm -rf /var/lib/apt/lists/* /usr/share/doc /usr/share/man /usr/share/locale \
            /lib/systemd/system /etc/systemd/system /usr/lib/systemd/user \
 && mkdir -p /data && chmod 700 /data
COPY --from=build /out/strawlog /usr/local/bin/strawlog
ENV STRAWLOG_DATA=/data \
    STRAWLOG_HOST_ROOT=/host
VOLUME ["/data"]
EXPOSE 8080 7443
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["strawlog", "healthcheck"]
ENTRYPOINT ["strawlog"]
CMD ["hub"]
