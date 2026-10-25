# An image holding just the ghzt binary, for other images to copy it from:
#   COPY --from=ghcr.io/evadcmd/ghzt:<tag> /ghzt /usr/local/bin/ghzt
# Built on the build platform and cross-compiled with GOOS / GOARCH, so a
# multi-platform build doesn't run the Go toolchain under emulation.
# Pinned to the go line in go.mod (1.27.3 has the crypto/x509 fix); bump both
# together.
FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/ghzt ./cmd/ghzt

FROM scratch
# Links the GHCR package to this repo.
LABEL org.opencontainers.image.source=https://github.com/evadcmd/ghzt
COPY --from=build /out/ghzt /ghzt
