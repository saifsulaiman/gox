package transform

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/goxlang/gox/internal/analyzer"
	"golang.org/x/tools/go/ast/astutil"
)

const defaultRuntimeImport = "github.com/goxlang/gox/internal/runtime"
const runtimeAlias = "goxrt"

// TransformConfig controls code generation and transformation behavior.
type TransformConfig struct {
	OutputDir          string // Directory to write transformed source files
	RuntimeImportPath  string // Custom runtime import path if specified
	PreserveComments   bool   // Preserve source comments in AST
	InjectRuntimeDebug bool   // Emit runtime logging for debugging
}

// Transformer rewrites Go source ASTs according to proven memory strategy decisions.
type Transformer struct {
	cfg             TransformConfig
	result          *analyzer.Result
	decisionsByPos  map[string]analyzer.AllocationDecision // "file:line:col" -> decision
	decisionsByLine map[string][]analyzer.AllocationDecision // "file:line" -> decisions
}

// NewTransformer creates a new Transformer for an analyzer Result.
func NewTransformer(cfg TransformConfig, result *analyzer.Result) *Transformer {
	if cfg.RuntimeImportPath == "" {
		cfg.RuntimeImportPath = defaultRuntimeImport
	}

	t := &Transformer{
		cfg:             cfg,
		result:          result,
		decisionsByPos:  make(map[string]analyzer.AllocationDecision),
		decisionsByLine: make(map[string][]analyzer.AllocationDecision),
	}

	if result != nil {
		for _, d := range result.Decisions {
			if d.Confidence != analyzer.ConfidenceProven {
				continue
			}
			t.decisionsByPos[d.SourcePosition] = d

			// Also index by file:line for position-tolerant matching
			pos := d.SourcePosition
			if idx := strings.LastIndex(pos, ":"); idx != -1 {
				linePos := pos[:idx]
				t.decisionsByLine[linePos] = append(t.decisionsByLine[linePos], d)
			}
		}
	}

	return t
}

// findDecision looks up an AllocationDecision matching a token.Position.
func (t *Transformer) findDecision(pos token.Position) *analyzer.AllocationDecision {
	key := fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Column)
	if d, ok := t.decisionsByPos[key]; ok {
		return &d
	}

	// Try matching by base filename:line:col
	baseKey := fmt.Sprintf("%s:%d:%d", filepath.Base(pos.Filename), pos.Line, pos.Column)
	for k, d := range t.decisionsByPos {
		if strings.HasSuffix(k, baseKey) {
			return &d
		}
	}

	// Try matching by file:line
	lineKey := fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
	if list, ok := t.decisionsByLine[lineKey]; ok && len(list) > 0 {
		return &list[0]
	}

	baseLineKey := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
	for k, list := range t.decisionsByLine {
		if strings.HasSuffix(k, baseLineKey) && len(list) > 0 {
			return &list[0]
		}
	}

	return nil
}

// TransformSource parses, transforms, and formats a single Go source string.
func (t *Transformer) TransformSource(src string, filename string) (string, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return "", false, fmt.Errorf("parse %s: %w", filename, err)
	}

	modified := t.TransformFile(fset, file, filename)
	if !modified {
		return src, false, nil
	}

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return "", false, fmt.Errorf("format %s: %w", filename, err)
	}

	return buf.String(), true, nil
}

// TransformFile modifies an AST File in-place based on proven memory decisions.
func (t *Transformer) TransformFile(fset *token.FileSet, file *ast.File, filename string) bool {
	transformed := false

	// Track which functions need a region arena injected
	needsArena := make(map[*ast.FuncDecl]bool)

	// Phase 1: Inspect functions for region requirements
	for _, decl := range file.Decls {
		fnDecl, ok := decl.(*ast.FuncDecl)
		if !ok || fnDecl.Body == nil {
			continue
		}

		ast.Inspect(fnDecl.Body, func(n ast.Node) bool {
			if n == nil {
				return true
			}
			pos := fset.Position(n.Pos())
			if d := t.findDecision(pos); d != nil && d.Strategy == analyzer.StrategyRegion {
				if d.Region == nil || d.Region.Scope != "CALLER" {
					needsArena[fnDecl] = true
				}
			}
			return true
		})
	}

	// Phase 2: Inject arena initializers at function entry
	for fnDecl := range needsArena {
		arenaInitStmt := &ast.AssignStmt{
			Lhs: []ast.Expr{ast.NewIdent("_goxArena")},
			Tok: token.DEFINE,
			Rhs: []ast.Expr{
				&ast.CallExpr{
					Fun: &ast.SelectorExpr{
						X:   ast.NewIdent(runtimeAlias),
						Sel: ast.NewIdent("NewArena"),
					},
				},
			},
		}

		arenaFreeStmt := &ast.DeferStmt{
			Call: &ast.CallExpr{
				Fun: &ast.SelectorExpr{
					X:   ast.NewIdent(runtimeAlias),
					Sel: ast.NewIdent("Free"),
				},
				Args: []ast.Expr{ast.NewIdent("_goxArena")},
			},
		}

		// Prepend to function body
		fnDecl.Body.List = append([]ast.Stmt{arenaInitStmt, arenaFreeStmt}, fnDecl.Body.List...)
		transformed = true
	}

	// Phase 3: Rewrite allocation expressions
	file = astutil.Apply(file, func(cursor *astutil.Cursor) bool {
		node := cursor.Node()
		if node == nil {
			return true
		}

		switch expr := node.(type) {
		case *ast.UnaryExpr:
			// Match address-of composite literal: &T{...}
			if expr.Op != token.AND {
				return true
			}
			lit, ok := expr.X.(*ast.CompositeLit)
			if !ok {
				return true
			}

			pos := fset.Position(expr.Pos())
			decision := t.findDecision(pos)
			if decision == nil {
				return true
			}

			switch decision.Strategy {
			case analyzer.StrategyImmortal:
				replacement := t.rewriteImmortal(lit, decision)
				cursor.Replace(replacement)
				transformed = true

			case analyzer.StrategyRegion:
				var replacement ast.Expr
				if decision.Region != nil && decision.Region.Scope == "CALLER" {
					replacement = t.rewriteUnique(lit)
				} else {
					replacement = t.rewriteRegion(lit)
				}
				cursor.Replace(replacement)
				transformed = true

			case analyzer.StrategyUniqueOwned:
				replacement := t.rewriteUnique(lit)
				cursor.Replace(replacement)
				transformed = true

			case analyzer.StrategyARC:
				replacement := t.rewriteARC(lit, decision)
				cursor.Replace(replacement)
				transformed = true
			}

		case *ast.CallExpr:
			ident, ok := expr.Fun.(*ast.Ident)
			if !ok {
				return true
			}

			if ident.Name == "new" && len(expr.Args) == 1 {
				pos := fset.Position(expr.Pos())
				decision := t.findDecision(pos)
				if decision == nil {
					return true
				}

				lit := &ast.CompositeLit{Type: expr.Args[0]}

				switch decision.Strategy {
				case analyzer.StrategyImmortal:
					replacement := t.rewriteImmortal(lit, decision)
					cursor.Replace(replacement)
					transformed = true

				case analyzer.StrategyRegion:
					var replacement ast.Expr
					if decision.Region != nil && decision.Region.Scope == "CALLER" {
						replacement = t.rewriteUnique(lit)
					} else {
						replacement = t.rewriteRegion(lit)
					}
					cursor.Replace(replacement)
					transformed = true

				case analyzer.StrategyUniqueOwned:
					replacement := t.rewriteUnique(lit)
					cursor.Replace(replacement)
					transformed = true

				case analyzer.StrategyARC:
					replacement := t.rewriteARC(lit, decision)
					cursor.Replace(replacement)
					transformed = true
				}
			} else if ident.Name == "make" && len(expr.Args) >= 2 {
				// Match make([]T, len, [cap])
				arrayType, ok := expr.Args[0].(*ast.ArrayType)
				if ok && arrayType.Len == nil {
					pos := fset.Position(expr.Pos())
					decision := t.findDecision(pos)
					if decision != nil && decision.Strategy == analyzer.StrategyRegion {
						replacement := t.rewriteMakeSlice(expr, arrayType)
						cursor.Replace(replacement)
						transformed = true
					}
				}
			}
		}

		return true
	}, nil).(*ast.File)

	// Phase 4: Inject runtime import if any transformation was performed
	if transformed {
		astutil.AddNamedImport(fset, file, runtimeAlias, t.cfg.RuntimeImportPath)
	}

	return transformed
}

// rewriteMakeSlice generates goxrt.AllocSlice[T](_goxArena, len, cap).
func (t *Transformer) rewriteMakeSlice(expr *ast.CallExpr, arrayType *ast.ArrayType) ast.Expr {
	lenExpr := expr.Args[1]
	capExpr := lenExpr
	if len(expr.Args) >= 3 {
		capExpr = expr.Args[2]
	}

	return &ast.CallExpr{
		Fun: &ast.IndexExpr{
			X: &ast.SelectorExpr{
				X:   ast.NewIdent(runtimeAlias),
				Sel: ast.NewIdent("AllocSlice"),
			},
			Index: arrayType.Elt,
		},
		Args: []ast.Expr{
			ast.NewIdent("_goxArena"),
			lenExpr,
			capExpr,
		},
	}
}

// rewriteImmortal generates goxrt.AllocReadOnly(val) or goxrt.AllocImmortal(val).
func (t *Transformer) rewriteImmortal(lit *ast.CompositeLit, decision *analyzer.AllocationDecision) ast.Expr {
	fnName := "AllocImmortal"
	if decision.Immortal != nil && decision.Immortal.IsReadOnly {
		fnName = "AllocReadOnly"
	}

	return &ast.CallExpr{
		Fun: &ast.SelectorExpr{
			X:   ast.NewIdent(runtimeAlias),
			Sel: ast.NewIdent(fnName),
		},
		Args: []ast.Expr{lit},
	}
}

// rewriteRegion generates goxrt.AllocVal(_goxArena, val).
func (t *Transformer) rewriteRegion(lit *ast.CompositeLit) ast.Expr {
	return &ast.CallExpr{
		Fun: &ast.SelectorExpr{
			X:   ast.NewIdent(runtimeAlias),
			Sel: ast.NewIdent("AllocVal"),
		},
		Args: []ast.Expr{
			ast.NewIdent("_goxArena"),
			lit,
		},
	}
}

// rewriteUnique generates goxrt.AllocUnique(val).
func (t *Transformer) rewriteUnique(lit *ast.CompositeLit) ast.Expr {
	return &ast.CallExpr{
		Fun: &ast.SelectorExpr{
			X:   ast.NewIdent(runtimeAlias),
			Sel: ast.NewIdent("AllocUnique"),
		},
		Args: []ast.Expr{lit},
	}
}

// rewriteARC generates goxrt.AllocARC(val, isAtomic).
func (t *Transformer) rewriteARC(lit *ast.CompositeLit, decision *analyzer.AllocationDecision) ast.Expr {
	isAtomic := false
	if decision.ARC != nil && decision.ARC.IsAtomic {
		isAtomic = true
	}

	atomicIdent := ast.NewIdent("false")
	if isAtomic {
		atomicIdent = ast.NewIdent("true")
	}

	return &ast.CallExpr{
		Fun: &ast.SelectorExpr{
			X:   ast.NewIdent(runtimeAlias),
			Sel: ast.NewIdent("AllocARC"),
		},
		Args: []ast.Expr{
			lit,
			atomicIdent,
		},
	}
}

// TransformDirectory recursively transforms all Go source files in inDir and writes them to outDir.
func (t *Transformer) TransformDirectory(inDir, outDir string) ([]string, error) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	entries, err := os.ReadDir(inDir)
	if err != nil {
		return nil, fmt.Errorf("read input dir: %w", err)
	}

	var transformedFiles []string

	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" || name == ".gox-cache" || name == "goxrt" || name == "vendor" {
			continue
		}

		inPath := filepath.Join(inDir, name)
		outPath := filepath.Join(outDir, name)

		if entry.IsDir() {
			subTransformed, err := t.TransformDirectory(inPath, outPath)
			if err != nil {
				return nil, err
			}
			transformedFiles = append(transformedFiles, subTransformed...)
			continue
		}

		if !strings.HasSuffix(name, ".go") {
			continue
		}

		content, err := os.ReadFile(inPath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", inPath, err)
		}

		transformedSrc, modified, err := t.TransformSource(string(content), inPath)
		if err != nil {
			return nil, err
		}

		if modified {
			transformedFiles = append(transformedFiles, outPath)
		}

		if err := os.WriteFile(outPath, []byte(transformedSrc), 0644); err != nil {
			return nil, fmt.Errorf("write %s: %w", outPath, err)
		}
	}

	return transformedFiles, nil
}
