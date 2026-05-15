# MFP Project Guide

## Project type

Go service implementing a Model Failover Proxy with OpenAI-compatible API endpoints and an admin UI.

## Common commands

- Run service: `go run ./cmd/mfp`
- Run mock provider: `go run ./cmd/mock-provider`
- Test: `go test ./...`
- Format: `gofmt -w ./cmd ./internal`
- Vet: `go vet ./...`
- Build binaries: `go build -o build/mfp ./cmd/mfp` and `go build -o build/mock-provider ./cmd/mock-provider`

## Structure

- `cmd/`: executable entry points
- `internal/`: private application packages
- `configs/`: example and development JSON configs
- `docs/`: project documentation beyond README/PRD
- `scripts/`: local automation scripts
- `deployments/`: deployment manifests and packaging assets
- `build/`: generated binaries and build output
- `data*/`, `logs/`, `tmp/`: local runtime state, ignored by git

## Development notes

- Keep external dependencies minimal unless they clearly simplify the implementation.
- Do not commit local secrets, runtime data, logs, or local config overrides.
- Preserve OpenAI-compatible behavior for `/v1/chat/completions` and `/v1/responses` when changing proxy code.
