# One static binary, one container (docs/self-hosting.md). Migrations are
# embedded and run on startup, so the image needs nothing else.
#
# Runtime env: DATABASE_URL, ORIGIN, PUBLIC_URL (required); LISTEN_ADDR
# (default :8080); TRUSTED_PROXIES (comma-separated CIDRs of the reverse
# proxy, default none: set it behind a proxy); limits, see docs/self-hosting.md.
# Health check: GET /healthz (pings PostgreSQL).
#
# Base images are pinned by digest (the tag is kept for readability). To
# update: docker buildx imagetools inspect <image:tag>, copy the digest.

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# Cross-compiled on the build platform: no emulation for the arm64 image.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# distroless/static: CA certs + tzdata, no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
