# ─── spa ──────────────────────────────────────────────────────────────────────
# Build the React/Vite SPA so its dist/ can be go:embed'd into the binary. dist/
# is gitignored, so it must be produced here rather than copied from the host.
FROM node:20-alpine AS spa
WORKDIR /spa
COPY internal/web/spa/package.json internal/web/spa/package-lock.json ./
RUN npm ci
COPY internal/web/spa/ ./
RUN npm run build

# ─── build ────────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Drop in the freshly built SPA so go:embed all:spa/dist resolves.
COPY --from=spa /spa/dist ./internal/web/spa/dist
# Pure-Go build (modernc sqlite) → static binary, no cgo. Templates + the SPA are
# baked in via go:embed, so the final image needs nothing but the binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /verix-dbm ./cmd/server
# Stage an empty /data so the runtime image owns it as nonroot (distroless has
# no shell/chown). A freshly-created named volume inherits this ownership.
RUN mkdir -p /data

# ─── runtime ──────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /verix-dbm /verix-dbm
# SQLite metadata lives here; mount a volume at /data in production. Owned by
# nonroot (uid 65532) so the app can create the DB + WAL/SHM files.
COPY --from=build --chown=nonroot:nonroot /data /data
VOLUME ["/data"]
ENV DBM_ADDR=:8080 DBM_SQLITE_PATH=/data/verix-dbm.db
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/verix-dbm"]
