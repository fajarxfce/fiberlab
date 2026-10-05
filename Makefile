.PHONY: build dev test check run doctor clean

build:
	cd web && npm ci && npm run build
	mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o bin/ftthlab ./cmd/ftthlab

dev:
	go run ./cmd/ftthlab serve --dev

test:
	go test -race ./...

check:
	go vet ./...
	cd web && npm run check

run:
	./bin/ftthlab serve

doctor:
	go run ./cmd/ftthlab doctor

clean:
	rm -rf bin web/dist
