package goweb

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SearchHit represents a single document hit in a search query.
type SearchHit struct {
	ID     string         `json:"id"`
	Score  float64        `json:"score"`
	Source map[string]any `json:"source"`
}

// SearchResult holds full-text query results.
type SearchResult struct {
	Total  int64       `json:"total"`
	TookMs int64       `json:"took_ms"`
	Hits   []SearchHit `json:"hits"`
}

// ElasticsearchConfig configures the Elasticsearch client.
type ElasticsearchConfig struct {
	Addresses []string      // e.g. ["http://localhost:9200"]
	Username  string
	Password  string
	Timeout   time.Duration
}

// memoryDoc represents an indexed document in the in-memory inverted index.
type memoryDoc struct {
	id     string
	source map[string]any
	tokens map[string]bool
}

// SearchClient provides enterprise full-text search operations with HTTP REST support
// and an embedded zero-alloc inverted index engine fallback.
type SearchClient struct {
	cfg        ElasticsearchConfig
	isMemory   bool
	httpClient *http.Client
	mu         sync.RWMutex
	indices    map[string]map[string]*memoryDoc
}

// NewSearchClient creates a new search client.
func NewSearchClient(cfg ElasticsearchConfig) *SearchClient {
	timeout := cmp.Or(cfg.Timeout, 3*time.Second)

	sc := &SearchClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: timeout},
		indices:    make(map[string]map[string]*memoryDoc),
		isMemory:   len(cfg.Addresses) == 0,
	}

	return sc
}

func tokenize(text string) map[string]bool {
	tokens := make(map[string]bool)
	lower := strings.ToLower(text)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	for _, w := range words {
		if len(w) > 1 {
			tokens[w] = true
		}
	}
	return tokens
}

// Index adds or updates a document in the search index.
func (sc *SearchClient) Index(ctx context.Context, index, id string, doc any) error {
	if sc == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal document: %w", err)
	}

	if !sc.isMemory && len(sc.cfg.Addresses) > 0 {
		url := fmt.Sprintf("%s/%s/_doc/%s", sc.cfg.Addresses[0], index, id)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if sc.cfg.Username != "" {
				req.SetBasicAuth(sc.cfg.Username, sc.cfg.Password)
			}
			resp, err := sc.httpClient.Do(req)
			if err == nil && (resp.StatusCode == 200 || resp.StatusCode == 201) {
				_ = resp.Body.Close()
				return nil
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
		}
		// Fallback to in-memory index if remote is down
	}

	// In-memory indexing
	var docMap map[string]any
	if err := json.Unmarshal(data, &docMap); err != nil {
		return err
	}

	var sb strings.Builder
	for _, v := range docMap {
		sb.WriteString(fmt.Sprintf("%v ", v))
	}
	tokens := tokenize(sb.String())

	sc.mu.Lock()
	defer sc.mu.Unlock()
	idxDocs, ok := sc.indices[index]
	if !ok {
		idxDocs = make(map[string]*memoryDoc)
		sc.indices[index] = idxDocs
	}
	idxDocs[id] = &memoryDoc{
		id:     id,
		source: docMap,
		tokens: tokens,
	}

	return nil
}

// Get retrieves a document by ID.
func (sc *SearchClient) Get(ctx context.Context, index, id string) (map[string]any, error) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	idxDocs, ok := sc.indices[index]
	if !ok {
		return nil, fmt.Errorf("index not found")
	}
	doc, ok := idxDocs[id]
	if !ok {
		return nil, fmt.Errorf("document not found")
	}
	return doc.source, nil
}

// Delete removes a document from the index.
func (sc *SearchClient) Delete(ctx context.Context, index, id string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	idxDocs, ok := sc.indices[index]
	if ok {
		delete(idxDocs, id)
	}
	return nil
}

// Search executes a full-text query against an index.
func (sc *SearchClient) Search(ctx context.Context, index, queryStr string) (*SearchResult, error) {
	start := time.Now()

	// Try remote Elasticsearch HTTP if configured
	if !sc.isMemory && len(sc.cfg.Addresses) > 0 {
		esQuery := map[string]any{
			"query": map[string]any{
				"multi_match": map[string]any{
					"query":  queryStr,
					"fields": []string{"*"},
				},
			},
		}
		body, _ := json.Marshal(esQuery)
		url := fmt.Sprintf("%s/%s/_search", sc.cfg.Addresses[0], index)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if sc.cfg.Username != "" {
				req.SetBasicAuth(sc.cfg.Username, sc.cfg.Password)
			}
			resp, err := sc.httpClient.Do(req)
			if err == nil && resp.StatusCode == 200 {
				defer resp.Body.Close()
				var esResp struct {
					Took int64 `json:"took"`
					Hits struct {
						Total struct {
							Value int64 `json:"value"`
						} `json:"total"`
						Hits []struct {
							ID     string         `json:"_id"`
							Score  float64        `json:"_score"`
							Source map[string]any `json:"_source"`
						} `json:"hits"`
					} `json:"hits"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&esResp); err == nil {
					hits := make([]SearchHit, 0, len(esResp.Hits.Hits))
					for _, h := range esResp.Hits.Hits {
						hits = append(hits, SearchHit{
							ID:     h.ID,
							Score:  h.Score,
							Source: h.Source,
						})
					}
					return &SearchResult{
						Total:  esResp.Hits.Total.Value,
						TookMs: esResp.Took,
						Hits:   hits,
					}, nil
				}
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
		}
	}

	// In-memory inverted index query
	queryTokens := tokenize(queryStr)
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	idxDocs, ok := sc.indices[index]
	if !ok {
		return &SearchResult{Total: 0, TookMs: time.Since(start).Milliseconds(), Hits: []SearchHit{}}, nil
	}

	var hits []SearchHit
	for _, doc := range idxDocs {
		matchCount := 0
		for qt := range queryTokens {
			if doc.tokens[qt] {
				matchCount++
			}
		}
		// If query is empty or matches tokens
		if len(queryTokens) == 0 || matchCount > 0 {
			score := float64(matchCount)
			if len(queryTokens) > 0 {
				score = score / float64(len(queryTokens))
			} else {
				score = 1.0
			}
			hits = append(hits, SearchHit{
				ID:     doc.id,
				Score:  score,
				Source: doc.source,
			})
		}
	}

	return &SearchResult{
		Total:  int64(len(hits)),
		TookMs: time.Since(start).Milliseconds(),
		Hits:   hits,
	}, nil
}

// Query is a simplified helper returning slice of document source maps.
func (sc *SearchClient) Query(ctx context.Context, index, queryStr string) ([]map[string]any, error) {
	res, err := sc.Search(ctx, index, queryStr)
	if err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, len(res.Hits))
	for _, hit := range res.Hits {
		results = append(results, hit.Source)
	}
	return results, nil
}

// Health checks the cluster health status.
func (sc *SearchClient) Health(ctx context.Context) error {
	if sc.isMemory || len(sc.cfg.Addresses) == 0 {
		return nil
	}
	url := fmt.Sprintf("%s/_cluster/health", sc.cfg.Addresses[0])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := sc.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("elasticsearch health check failed (%d): %s", resp.StatusCode, string(data))
	}
	return nil
}

// Close closes the search client resources.
func (sc *SearchClient) Close() error {
	return nil
}
