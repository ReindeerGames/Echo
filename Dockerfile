FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/echo ./cmd/echo

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --create-home echo
WORKDIR /app
COPY --from=build /out/echo /usr/local/bin/echo
RUN mkdir -p /var/lib/echo/workers && chown -R echo:echo /var/lib/echo
USER echo
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/echo"]
