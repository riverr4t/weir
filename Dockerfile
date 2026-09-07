# Weir: one static Go binary, scratch runtime (spec §9).
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /weir ./cmd/weir

FROM alpine:3.20 AS certs
RUN apk add --no-cache ca-certificates tzdata

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=certs /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /weir /weir
USER 1000:1000
EXPOSE 3004
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/weir", "healthcheck"]
ENTRYPOINT ["/weir"]
