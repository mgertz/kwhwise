FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN mkdir -p /licenses && cp /usr/local/go/LICENSE /licenses/go.LICENSE && \
    for module in github.com/eclipse/paho.mqtt.golang go.yaml.in/yaml/v3 github.com/gorilla/websocket golang.org/x/net golang.org/x/sync; do \
      directory="$(go list -m -f '{{.Dir}}' "$module")"; \
      cp "$directory/LICENSE" "/licenses/$(basename "$directory").LICENSE"; \
    done
COPY cmd ./cmd
COPY internal ./internal
COPY profiles ./profiles
ARG TARGETOS
ARG TARGETARCH
ARG APP_VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/mgertz/kwhwise/internal/buildinfo.Version=$APP_VERSION" -o /kwhwise ./cmd/kwhwise

FROM scratch
ARG APP_VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/mgertz/kwhwise" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="$APP_VERSION"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /kwhwise /kwhwise
COPY LICENSE /LICENSE
COPY NOTICE /NOTICE
COPY --from=build /licenses /licenses
ENV TZ=Europe/Copenhagen
USER 65532:65532
ENTRYPOINT ["/kwhwise"]
