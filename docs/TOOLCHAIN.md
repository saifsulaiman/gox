# GOX Toolchain, Caching & Cross-Compilation Guide

## 1. Overview

GOX v0.9.5 hardens the GOX compiler toolchain into an industrial-grade compilation pipeline. It introduces:
1. **Incremental Build Decision Caching**: Sub-millisecond rebuilds via SHA-256 content hashing.
2. **First-Class Cross-Compilation**: Target any operating system and architecture (`GOOS`, `GOARCH`) with full SSA architecture layout accuracy.
3. **Compiler & Linker Flag Passthrough**: Forward `-tags`, `-ldflags`, and `-gcflags` seamlessly.
4. **Multi-Package & Vendor Hardening**: Compile complex multi-package applications with automatic `./...` analysis while keeping `vendor/` untouched.

---

## 2. Incremental Build Caching Architecture

Static analysis, escape analysis, and whole-program memory strategy proofs involve compute-intensive SSA construction and dataflow graph traversal. To ensure rapid developer edit-compile-test iteration loops, GOX v0.9.5 includes a deterministic **decision cache**.

```
 +---------------------------------------------------------+
 |                      Source Tree                        |
 |  - All *.go files across project                        |
 |  - go.mod / go.sum                                      |
 |  - Target Environment (GOOS, GOARCH)                    |
 |  - Build Flags (-tags)                                  |
 +----------------------------+----------------------------+
                              |
                     SHA-256 Fingerprint
                              |
                              v
                .gox-cache/decisions/<hash>.json
                              |
               +--------------+--------------+
               |                             |
          Cache Hit                     Cache Miss
               |                             |
      Load JSON Decisions            Full SSA Strategy
       (0.0004 seconds)              Analysis & Verification
               |                             |
               +--------------+--------------+
                              |
                              v
                  AST Code Transformation
                              |
                              v
                  Native Go Toolchain Build
```

### Cache Key Computation
The cache fingerprint is computed via:
$$\text{Key} = \text{SHA256}(\text{Version} \mathbin{\Vert} \text{Patterns} \mathbin{\Vert} \text{TargetEnv} \mathbin{\Vert} \text{BuildFlags} \mathbin{\Vert} \sum \text{FileContent})$$

- **Automatic Invalidation**: Any edit to a `.go` file or `go.mod` immediately changes the hash, preventing stale builds.
- **Atomic Persistence**: Cache writes are written to temporary files and renamed atomically, preventing partial or corrupted cache reads.
- **Cache Bypass**: Pass `-no-cache` to force clean re-analysis.

---

## 3. Cross-Compilation (`GOOS` and `GOARCH`)

GOX supports full cross-compilation for all Go-supported target operating systems (Linux, Windows, Darwin, FreeBSD, etc.) and architectures (amd64, arm64, 386, arm, etc.).

### Architecture-Aware Analysis
Cross-compilation in GOX is not merely passing `GOOS`/`GOARCH` to the linker:
- `packages.Load` is executed with the target architecture environment.
- The Go type checker and SSA builder use target pointer sizes (32-bit vs. 64-bit), struct field alignments, and target-specific conditional compilation tags (`//go:build linux`).
- Escape analysis and memory strategy proofs accurately reflect target machine constraints.

### Usage
```bash
# Cross-compile for Linux 64-bit (ELF executable)
gox build -os linux -arch amd64 -o bin/server-linux .

# Cross-compile for Windows 64-bit (PE executable)
gox build -os windows -arch amd64 -o bin/server.exe .

# Using standard Go environment variables
GOOS=linux GOARCH=arm64 gox build -o bin/server-arm64 .
```

---

## 4. Multi-Package Module Support

In multi-package projects:
```
myproject/
  go.mod
  main.go
  config/
    config.go
  worker/
    worker.go
```

Running `gox build .` or `gox run .`:
1. Automatically expands module analysis across all internal packages (`./...`), identifying and proving allocations across all package boundaries.
2. Generates a unified, self-contained runtime module (`goxrt`) at the module root.
3. Automatically rewrites subpackages to import `<module>/goxrt`.
4. Skips the `vendor/` directory, preserving third-party external dependencies in standard Go without modification.

---

## 5. Build Flags Passthrough

| Flag | Description | Forwarded To |
| :--- | :--- | :--- |
| `-tags <tags>` | Comma-separated build tags | GOX Analyzer & `go build -tags` |
| `-ldflags <flags>` | Linker flags (e.g. `-s -w -X main.version=1.0`) | `go build -ldflags` |
| `-gcflags <flags>` | Compiler flags | `go build -gcflags` |
| `-no-cache` | Disable incremental decision cache | GOX Build Engine |
| `-keep-transformed` | Retain transformed tree in `.gox-cache/staging` | GOX Staging Engine |
| `-v` | Verbose compilation output | GOX Logger |

---

## 6. Direct Application Execution (`gox run`)

The `gox run` command provides an immediate edit-compile-run loop analogous to `go run`, while transparently applying the full GOX deterministic memory optimization pipeline.

```bash
# Basic run
gox run .

# Run with application arguments (forwarded after '--')
gox run . -- -port 8080 -db app.db

# Run from an external directory
gox run -dir ./examples/todo-htmx . -- -port 8080 -in-memory
```

### Execution Lifecycle:
1. **Target Identification**: Resolves module roots and source packages.
2. **Analysis & Staging**: Analyzes allocation lifecycles and creates an isolated ephemeral staging directory (`os.MkdirTemp("", "gox-run-*")`).
3. **AST Transformation**: Rewrites proven allocation sites and embeds the `goxrt` runtime.
4. **Compilation**: Compiles an ephemeral binary directly in the staging folder.
5. **Execution & Argument Forwarding**: Executes the binary directly, piping `os.Stdin`, `os.Stdout`, and `os.Stderr`, and passing all arguments specified after `--`.
6. **Automatic Cleanup**: Deletes the ephemeral binary and temporary staging directory upon process exit or interruption.

For complete recipes, Docker containerization, and hot reloading configurations, refer to [Running Go Applications with GOX](RUN_GUIDE.md).
