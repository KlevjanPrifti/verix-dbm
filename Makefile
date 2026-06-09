.PHONY: build spa run vet test vuln tidy

# Build the SPA (embedded) then the static Go binary.
build: spa
	CGO_ENABLED=0 go build -o bin/verix-dbm ./cmd/server

spa:
	npm --prefix internal/web/spa ci
	npm --prefix internal/web/spa run build

run:
	go run ./cmd/server

vet:
	go vet ./...

test:
	go test ./...

# Dependency vulnerability scan. Run locally and in CI.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

tidy:
	go mod tidy
