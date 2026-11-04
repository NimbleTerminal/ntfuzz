BINARY ?= bin/ntfuzz
CONFIG ?= configs/projects.yaml
DB ?= ntfuzz.duckdb
LISTEN ?= 127.0.0.1:8080

.PHONY: all build test serve tui clean

all: build

build:
	@mkdir -p bin
	go build -o $(BINARY) ./cmd/ntfuzz

test:
	go test -v ./...

serve: build
	./$(BINARY) serve --config $(CONFIG) --db $(DB) --listen $(LISTEN)

tui: build
	./$(BINARY) tui --api http://$(LISTEN)

clean:
	rm -rf bin/ .ntfuzz/ *.duckdb *.duckdb.wal
