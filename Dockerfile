# ─── build ────────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure-Go build (modernc sqlite) → static binary, no cgo. Templates/static are
# baked in via go:embed, so the final image needs nothing but the binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /verix-dbm ./cmd/server

# ─── runtime ──────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /verix-dbm /verix-dbm
# SQLite metadata lives here; mount a volume at /data in production.
VOLUME ["/data"]
ENV DBM_ADDR=:8080 DBM_SQLITE_PATH=/data/verix-dbm.db
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/verix-dbm"]
