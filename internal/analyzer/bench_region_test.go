package analyzer

import (
	"testing"

	goxruntime "github.com/goxlang/gox/internal/runtime"
)

// Simulated HTTP Request Workload
type RequestPayload struct {
	Path    string
	Headers map[string]string
	Body    []byte
}

type ResponseDTO struct {
	StatusCode int
	Message    string
	Data       []string
}

// BenchmarkHTTPRequestStandardHeap simulates an HTTP server handling requests
// using normal Go heap allocations (subject to GC).
func BenchmarkHTTPRequestStandardHeap(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		req := &RequestPayload{
			Path: "/api/v1/users",
			Body: []byte(`{"user_id": 12345, "action": "query"}`),
		}
		resp := &ResponseDTO{
			StatusCode: 200,
			Message:    "OK",
			Data:       []string{req.Path, string(req.Body)},
		}
		_ = resp
	}
}

// BenchmarkHTTPRequestRegionArena simulates an HTTP server handling requests
// using a request-scoped region arena that resets per request (zero GC).
func BenchmarkHTTPRequestRegionArena(b *testing.B) {
	arena := goxruntime.NewArena()
	defer arena.Free()

	b.ReportAllocs()
	for b.Loop() {
		req := goxruntime.Alloc[RequestPayload](arena)
		req.Path = "/api/v1/users"
		req.Body = []byte(`{"user_id": 12345, "action": "query"}`)

		resp := goxruntime.Alloc[ResponseDTO](arena)
		resp.StatusCode = 200
		resp.Message = "OK"
		resp.Data = []string{req.Path, string(req.Body)}

		_ = resp
		arena.Reset() // O(1) bulk reset per request
	}
}

// Simulated Parser AST Workload
type ASTNode struct {
	Kind     string
	Literal  string
	Children []*ASTNode
}

func buildTestAST(depth int) *ASTNode {
	if depth <= 0 {
		return &ASTNode{Kind: "IDENT", Literal: "x"}
	}
	return &ASTNode{
		Kind:    "BINARY_EXPR",
		Literal: "+",
		Children: []*ASTNode{
			buildTestAST(depth - 1),
			buildTestAST(depth - 1),
		},
	}
}

func buildTestASTArena(arena *goxruntime.Arena, depth int) *ASTNode {
	if depth <= 0 {
		n := goxruntime.Alloc[ASTNode](arena)
		n.Kind = "IDENT"
		n.Literal = "x"
		return n
	}
	n := goxruntime.Alloc[ASTNode](arena)
	n.Kind = "BINARY_EXPR"
	n.Literal = "+"
	n.Children = []*ASTNode{
		buildTestASTArena(arena, depth-1),
		buildTestASTArena(arena, depth-1),
	}
	return n
}

// BenchmarkParserASTStandardHeap simulates compiling AST trees on standard Go heap.
func BenchmarkParserASTStandardHeap(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		root := buildTestAST(8)
		_ = root
	}
}

// BenchmarkParserASTRegionArena simulates compiling AST trees in a region arena
// reclaimed as a single unit upon file completion.
func BenchmarkParserASTRegionArena(b *testing.B) {
	arena := goxruntime.NewArena()
	defer arena.Free()

	b.ReportAllocs()
	for b.Loop() {
		root := buildTestASTArena(arena, 8)
		_ = root
		arena.Reset()
	}
}

// BenchmarkRegionAnalysisOverhead measures analyzer latency when inferring
// both stack and region placement across an application package.
func BenchmarkRegionAnalysisOverhead(b *testing.B) {
	dir := b.TempDir()
	writeTestFile(b, dir, "go.mod", "module example.com/benchregion\n\ngo 1.24\n")
	writeTestFile(b, dir, "app.go", `package benchregion

type Item struct { ID int }

func NewItem(id int) *Item { return &Item{ID: id} }
func Local() int { p := new(int); *p = 1; return *p }
func Process() int {
	item := NewItem(10)
	return item.ID + Local()
}
`)

	b.ReportAllocs()
	for b.Loop() {
		_, err := Analyze(Config{Dir: dir, Patterns: []string{"./..."}, MemoryStrategy: true})
		if err != nil {
			b.Fatal(err)
		}
	}
}
