.PHONY: build test vet run docker tidy clean

BIN   := validator-update-agent
PKG   := ./cmd/agent
IMAGE ?= ghcr.io/upflam/validator-update-agent

build:
	go build -o $(BIN) $(PKG)

test:
	go test ./...

vet:
	go vet ./...

# Run locally. Set config via the environment (see examples/.env.example); --dry-run previews.
run: build
	./$(BIN)

docker:
	docker build -t $(IMAGE):latest .

tidy:
	go mod tidy

clean:
	rm -f $(BIN)
