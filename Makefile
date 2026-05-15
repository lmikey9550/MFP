.PHONY: run test fmt vet build clean

run:
	go run ./cmd/mfp

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

build:
	go build -o build/mfp ./cmd/mfp

clean:
	rm -rf build tmp
