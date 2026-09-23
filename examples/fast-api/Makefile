.PHONY: all run build bench doctor gox-build

all: build

run:
	go run .

build:
	go build -o bin/server .

gox-build:
	../../bin/gox build -o bin/gox-server .

bench:
	go run ./bench/runner

doctor:
	../../bin/gox doctor ./...
