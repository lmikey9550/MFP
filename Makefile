.PHONY: run mock test fmt vet build clean

run:
	go run ./cmd/mfp

mock:
	go run ./cmd/mock-provider

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

build:
	go build -o build/mfp ./cmd/mfp
	go build -o build/mock-provider ./cmd/mock-provider

clean:
	rm -rf build tmp
