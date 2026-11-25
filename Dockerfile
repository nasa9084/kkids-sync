# syntax=docker/dockerfile:1
# Two images from one file:
#   docker build --target web .         -> kkids-sync-web (static binary, distroless)
#   docker build --target reconciler .  -> kkids-sync-reconciler (Chromium + Xvfb)

FROM golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Shown at the bottom of the admin pages; the image workflow passes the tag.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/nasa9084/kkids-sync/internal/webui.Version=${VERSION}" -o /out/kkids-sync-web ./cmd/kkids-sync-web \
 && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/kkids-sync-reconciler ./cmd/kkids-sync-reconciler

# The Playwright driver and Chromium that match the playwright-go version in go.mod.
FROM build AS playwright
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
# Only the full Chromium: the reconciler runs headful under Xvfb.
RUN go run github.com/mxschmitt/playwright-go/cmd/playwright install --no-shell chromium \
 && chmod -R a+rX /opt/playwright-driver /opt/ms-playwright

FROM gcr.io/distroless/static-debian12:nonroot AS web
COPY --from=build /out/kkids-sync-web /usr/local/bin/kkids-sync-web
ENV KKIDS_DB=/data/kkids-sync.db \
    KKIDS_COVER_DIR=/data/covers \
    KKIDS_LISTEN=:8080 \
    KKIDS_INTERNAL_LISTEN=:8081
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/kkids-sync-web"]
CMD ["serve"]

FROM debian:bookworm-slim AS reconciler
ENV PLAYWRIGHT_DRIVER_PATH=/opt/playwright-driver \
    PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    DEBIAN_FRONTEND=noninteractive
# Made readable by everyone in the playwright stage: the job runs as 65532.
COPY --from=playwright /opt/playwright-driver /opt/playwright-driver
COPY --from=playwright /opt/ms-playwright /opt/ms-playwright
# Chromium's system libraries (as Playwright lists them), Xvfb for a headful
# browser, Japanese fonts so the dashboard renders as it does for a person, and
# x11vnc/noVNC for signing in.
RUN apt-get update \
 && /opt/playwright-driver/node /opt/playwright-driver/package/cli.js install-deps chromium \
 && apt-get install -y --no-install-recommends xvfb xauth fonts-noto-cjk ca-certificates tini \
      x11vnc novnc websockify \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 65532 --create-home --home-dir /home/nonroot nonroot
COPY --from=build /out/kkids-sync-reconciler /usr/local/bin/kkids-sync-reconciler
# Signing in from inside the cluster (x11vnc + noVNC): see docs/deploy.md.
COPY kkids-sync-login.sh /usr/local/bin/kkids-sync-login
USER 65532
WORKDIR /data
ENTRYPOINT ["/usr/bin/tini", "--", "xvfb-run", "--auto-servernum", "--server-args=-screen 0 1366x900x24", "/usr/local/bin/kkids-sync-reconciler"]
CMD ["-state", "/data/storage-state.json", "-out", "/data/snapshot.json", "run"]
