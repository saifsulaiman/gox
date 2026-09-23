.PHONY: build test test-race test-all coverage benchmark lint vulncheck vulncheck-verbose example bench-comparative release todo-test todo-bench todo-run

build:
	go build -o bin/gox ./cmd/gox

test:
	go test ./cmd/... ./internal/... ./pkg/...

test-race:
	go test -count=1 -race ./cmd/... ./internal/... ./pkg/...

test-all: test-race
	cd examples/goweb-app && go test -count=1 -v ./...

coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./cmd/... ./internal/... ./pkg/...
	go tool cover -func=coverage.out
	@rm -f coverage.out

benchmark:
	go test -bench=. -benchmem ./benchmarks

bench-comparative:
	go run ./benchmarks/runner

lint:
	go vet ./...

vulncheck:
	govulncheck ./...

vulncheck-verbose:
	govulncheck -show verbose ./...

release:
	@mkdir -p bin/release
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o bin/release/gox-darwin-arm64 ./cmd/gox
	GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o bin/release/gox-darwin-amd64 ./cmd/gox
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/release/gox-linux-amd64 ./cmd/gox
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/release/gox-linux-arm64 ./cmd/gox
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o bin/release/gox-windows-amd64.exe ./cmd/gox
	@echo "GOX v1.0 release artifacts successfully built in bin/release/"

example: build
	./bin/gox analyze -detail -dir ./examples/basic ./...
	./bin/gox analyze -memory-strategy -explain -dir ./examples/basic .
	./bin/gox analyze -memory-strategy -explain -graph bin/gox-ownership.dot -dir ./examples/ownership .
	./bin/gox build -v -dir ./examples/basic -o bin/basic-transformed .
	./bin/basic-transformed
	./bin/gox run -dir ./examples/basic .
	./bin/gox run -dir ./examples/pipeline .
	./bin/gox build -v -dir ./examples/todo-htmx -o bin/todo-transformed .

todo-test:
	cd examples/todo-htmx && go test -v ./...

todo-bench:
	cd examples/todo-htmx && go run ./bench/runner/main.go 2000

todo-run:
	cd examples/todo-htmx && go run .

gox-todo-run: build
	./bin/gox run -dir ./examples/todo-htmx .

gox-todo-bench: build
	./bin/gox run -dir ./examples/todo-htmx ./bench/runner/main.go -- 2000

doctor-todo: build
	./bin/gox doctor -dir ./examples/todo-htmx ./...

doctor-pipeline: build
	./bin/gox doctor -dir ./examples/pipeline ./...

fast-api-bench:
	cd examples/fast-api && go run ./bench/runner

fast-api-run:
	cd examples/fast-api && go run .

todo-sqlc-bench:
	cd examples/todo-sqlc && go run ./bench/runner/main.go 2000

todo-sqlc-run:
	cd examples/todo-sqlc && go run .

doctor-todo-sqlc: build
	./bin/gox doctor -dir ./examples/todo-sqlc ./db ./handlers .

goweb-test:
	go test -v ./pkg/goweb/...

goweb-bench:
	cd examples/goweb-app && go run ./bench/runner -n 20000 -c 8

goweb-run:
	cd examples/goweb-app && go run .

gox-goweb-bench: build
	./bin/gox run -dir ./examples/goweb-app ./bench/runner -- -n 20000 -c 8

gox-goweb-run: build
	./bin/gox run -dir ./examples/goweb-app .

doctor-goweb: build
	./bin/gox doctor -dir ./examples/goweb-app .

goweb-build-gox:
	./bin/gox build -v -dir ./examples/goweb-app -o bin/nexus-api-gox .