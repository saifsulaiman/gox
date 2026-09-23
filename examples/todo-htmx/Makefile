.PHONY: run test bench load-bench bench-comparative build gox-build gox-run

run:
	go run .

test:
	go test -v ./...

bench:
	go test -bench=. -benchmem ./bench

load-bench:
	go run ./bench/runner/main.go 2000

bench-comparative:
	cd ../.. && make bench-comparative

build:
	go build -o bin/todo .

gox-build:
	cd ../.. && ./bin/gox build -v -dir ./examples/todo-htmx -o bin/todo-transformed .

gox-run:
	cd ../.. && ./bin/gox run -dir ./examples/todo-htmx .

gox-load-bench:
	cd ../.. && ./bin/gox run -dir ./examples/todo-htmx ./bench/runner/main.go -- 2000
