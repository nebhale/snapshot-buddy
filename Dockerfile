# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY LICENSE ./LICENSE
COPY scripts/go-notices.sh /tmp/go-notices.sh
RUN sh /tmp/go-notices.sh
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /snapshot-buddy ./cmd/snapshot-buddy

FROM build AS test-build
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go test -c -o /buddy-test ./internal/buddy

FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0 AS runtime
RUN apk add --no-cache ca-certificates ffmpeg tzdata \
    && addgroup -g 10001 buddy \
    && adduser -D -u 10001 -G buddy buddy \
    && mkdir -p /data /config \
    && chown buddy:buddy /data
COPY --from=build /snapshot-buddy /usr/local/bin/snapshot-buddy
COPY --from=build /notices /usr/share/licenses/snapshot-buddy
COPY LICENSE THIRD_PARTY.md /usr/share/licenses/snapshot-buddy/
COPY licenses /usr/share/licenses/snapshot-buddy/licenses
USER 10001:10001
EXPOSE 8080/tcp 8514/udp
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["snapshot-buddy", "-healthcheck"]
ENTRYPOINT ["snapshot-buddy"]

# Exercise the exact runtime libraries without shipping tests in the release.
FROM runtime AS test
COPY --from=test-build /buddy-test /usr/local/bin/buddy-test
ENV REQUIRE_FFMPEG=1
HEALTHCHECK NONE
ENTRYPOINT ["buddy-test"]

FROM runtime AS release
