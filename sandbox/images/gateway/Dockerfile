# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS gateway-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/echo-egress ./cmd/echo-egress
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/echo-egress ./cmd/echo-egress

FROM alpine:3.22
ARG ECHO_SOURCE_REPOSITORY=https://github.com/BrentFarris/echo
RUN apk add --no-cache ca-certificates \
 && install -d -m 0700 /var/lib/echo-egress
COPY --from=gateway-build /out/echo-egress /usr/local/bin/echo-egress
LABEL org.opencontainers.image.title="Echo Sandbox Egress Gateway" \
      org.opencontainers.image.source="$ECHO_SOURCE_REPOSITORY"
EXPOSE 53 53/udp 1080 1081 3128 3129
ENTRYPOINT ["/usr/local/bin/echo-egress"]
