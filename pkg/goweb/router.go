package goweb

import (
	"net/http"
	"slices"
	"strings"
)

// nodeType represents the type of a router node.
type nodeType uint8

const (
	staticNode nodeType = iota
	paramNode
	catchAllNode
)

// routeNode represents a single node in the routing radix trie.
type routeNode struct {
	path      string
	part      string
	nType     nodeType
	paramName string
	children  []*routeNode
	handlers  map[string][]HandlerFunc
}

// newRouteNode creates an empty route node.
func newRouteNode(part string, nType nodeType) *routeNode {
	return &routeNode{
		part:     part,
		nType:    nType,
		handlers: make(map[string][]HandlerFunc),
	}
}

// Router is a radix-tree based HTTP request router.
type Router struct {
	root           *routeNode
	methodNotAllow bool
	notFound       []HandlerFunc
}

// NewRouter creates a new initialized Router.
func NewRouter() *Router {
	return &Router{
		root: newRouteNode("", staticNode),
	}
}

// AddRoute registers handlers for a given HTTP method and pattern.
func (r *Router) AddRoute(method, pattern string, handlers []HandlerFunc) {
	if pattern == "" || pattern[0] != '/' {
		pattern = "/" + pattern
	}
	parts := parsePattern(pattern)
	current := r.root

	for _, part := range parts {
		var child *routeNode
		nType := staticNode
		paramName := ""

		if strings.HasPrefix(part, ":") {
			nType = paramNode
			paramName = part[1:]
		} else if strings.HasPrefix(part, "*") {
			nType = catchAllNode
			paramName = part[1:]
		}

		idx := slices.IndexFunc(current.children, func(c *routeNode) bool {
			return c.part == part
		})
		if idx != -1 {
			child = current.children[idx]
		} else {
			child = newRouteNode(part, nType)
			child.paramName = paramName
			current.children = append(current.children, child)
		}
		current = child
	}

	current.path = pattern
	current.handlers[method] = handlers
}

// Match searches for the route node matching the given method and path.
// It fills the provided Params slice without heap allocations when capacity allows.
func (r *Router) Match(method, path string, params *Params) []HandlerFunc {
	parts := parsePattern(path)
	current := r.root

	for i, part := range parts {
		var matched *routeNode
		for _, child := range current.children {
			if child.nType == staticNode && child.part == part {
				matched = child
				break
			}
		}

		if matched == nil {
			// Try param match
			for _, child := range current.children {
				if child.nType == paramNode {
					matched = child
					if params != nil {
						*params = append(*params, Param{Key: child.paramName, Value: part})
					}
					break
				} else if child.nType == catchAllNode {
					matched = child
					if params != nil {
						// Join the rest of the path parts
						tail := strings.Join(parts[i:], "/")
						*params = append(*params, Param{Key: child.paramName, Value: tail})
					}
					// CatchAll consumes the entire remainder
					if handlers, ok := matched.handlers[method]; ok {
						return handlers
					}
					return nil
				}
			}
		}

		if matched == nil {
			return nil
		}
		current = matched
	}

	if handlers, ok := current.handlers[method]; ok {
		return handlers
	}
	return nil
}

func parsePattern(pattern string) []string {
	pattern = strings.Trim(pattern, "/")
	if pattern == "" {
		return nil
	}
	rawParts := strings.Split(pattern, "/")
	parts := make([]string, 0, len(rawParts))
	for _, p := range rawParts {
		if p != "" {
			parts = append(parts, p)
			if strings.HasPrefix(p, "*") {
				break
			}
		}
	}
	return parts
}

// ServeHTTP implements standard http.Handler for the router.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var params Params
	handlers := r.Match(req.Method, req.URL.Path, &params)
	if handlers == nil {
		if r.notFound != nil {
			c := newContext(w, req, nil, nil)
			c.handlers = r.notFound
			_ = c.Next()
			return
		}
		http.NotFound(w, req)
		return
	}

	c := newContext(w, req, nil, nil)
	c.Params = params
	c.handlers = handlers
	_ = c.Next()
}
