# Limelit Open, as a container.
#
# Two stages. The first builds the static binary; the second is a minimal
# image carrying that binary and Litestream. Litestream is what makes SQLite
# safe on a platform that can replace the container at any moment: it streams
# every change to an object-store bucket and restores from it on boot, so a
# restart loses nothing. Without it a Cloud Run instance would come back empty.
#
# The binary is the same one `go install` produces. Nothing in this image is a
# demo-only build; demo mode is one environment variable on the same binary.

# The build stage runs on the builder's own platform and cross-compiles, so an
# arm64 image builds in seconds on an amd64 runner instead of under emulation.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The commit is passed in rather than read from .git, which is excluded from
# the build context. It is what the demo banner shows and links to, so a
# build without it would ship a demo that cannot prove which code it runs.
# A release passes its tag as VERSION and the commit as COMMIT; Cloud Build
# passes the commit as VERSION alone, and the binary reads it as both.
ARG VERSION=dev
ARG COMMIT=
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/limelit ./cmd/limelit

FROM litestream/litestream:0.3 AS litestream

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 limelit
COPY --from=build /out/limelit /usr/local/bin/limelit
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY deploy/litestream.yml /etc/litestream.yml
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint
RUN chmod +x /usr/local/bin/entrypoint && mkdir -p /data && chown limelit:limelit /data
USER limelit
ENV LIMELIT_DATA_DIR=/data
EXPOSE 1515
ENTRYPOINT ["/usr/local/bin/entrypoint"]
