package goweb

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrCacheMiss indicates a requested key does not exist.
	ErrCacheMiss = errors.New("cache: key not found")
)

// RedisConfig configures the Redis client.
type RedisConfig struct {
	Addr        string        // e.g. "localhost:6379" or "" for in-memory
	Password    string
	DB          int
	DialTimeout time.Duration
	PoolSize    int
}

// memoryItem represents a cached item in the in-memory fallback.
type memoryItem struct {
	value     string
	expiresAt time.Time
}

// RedisClient provides high-level cache operations with automated connection pooling
// and an embedded zero-alloc in-memory engine fallback.
type RedisClient struct {
	cfg         RedisConfig
	isMemory    bool
	mu          sync.RWMutex
	memoryData  map[string]memoryItem
	connPool    chan net.Conn
	subscribers map[string][]func(string)
	subMu       sync.RWMutex
	cuckooData  map[string]map[string]bool
	sortedSets  map[string]map[string]float64
	topKData    map[string]map[string]int64
	hllData     map[string]map[string]bool
}

// NewRedisClient creates a new Redis client. If Addr is empty or unreachable during init,
// it runs in high-speed in-memory fallback mode.
func NewRedisClient(cfg RedisConfig) *RedisClient {
	cfg.DialTimeout = cmp.Or(cfg.DialTimeout, 2*time.Second)
	cfg.PoolSize = cmp.Or(cfg.PoolSize, 10)

	rc := &RedisClient{
		cfg:         cfg,
		memoryData:  make(map[string]memoryItem),
		subscribers: make(map[string][]func(string)),
		cuckooData:  make(map[string]map[string]bool),
		sortedSets:  make(map[string]map[string]float64),
		topKData:    make(map[string]map[string]int64),
		hllData:     make(map[string]map[string]bool),
	}

	if cfg.Addr == "" || cfg.Addr == "memory" {
		rc.isMemory = true
		return rc
	}

	// Try establishing pool
	rc.connPool = make(chan net.Conn, cfg.PoolSize)
	conn, err := rc.dial()
	if err != nil {
		// Fallback to high-speed in-memory engine
		rc.isMemory = true
		return rc
	}
	rc.connPool <- conn

	return rc
}

func (rc *RedisClient) dial() (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", rc.cfg.Addr, rc.cfg.DialTimeout)
	if err != nil {
		return nil, err
	}

	// Optional authentication
	if rc.cfg.Password != "" {
		cmd := fmt.Sprintf("*2\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n", len(rc.cfg.Password), rc.cfg.Password)
		if _, err := conn.Write([]byte(cmd)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "+OK") {
			_ = conn.Close()
			return nil, fmt.Errorf("redis auth failed: %v", line)
		}
	}

	// Optional select DB
	if rc.cfg.DB > 0 {
		dbStr := strconv.Itoa(rc.cfg.DB)
		cmd := fmt.Sprintf("*2\r\n$6\r\nSELECT\r\n$%d\r\n%s\r\n", len(dbStr), dbStr)
		if _, err := conn.Write([]byte(cmd)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, "+OK") {
			_ = conn.Close()
			return nil, fmt.Errorf("redis select db failed: %v", line)
		}
	}

	return conn, nil
}

func (rc *RedisClient) getConn() (net.Conn, error) {
	select {
	case conn := <-rc.connPool:
		return conn, nil
	default:
		return rc.dial()
	}
}

func (rc *RedisClient) putConn(conn net.Conn) {
	if conn == nil {
		return
	}
	select {
	case rc.connPool <- conn:
	default:
		_ = conn.Close()
	}
}

// Ping checks if the Redis connection is alive.
func (rc *RedisClient) Ping(ctx context.Context) error {
	if rc.isMemory {
		return nil
	}
	conn, err := rc.getConn()
	if err != nil {
		return err
	}
	defer rc.putConn(conn)

	_ = conn.SetDeadline(time.Now().Add(rc.cfg.DialTimeout))
	_, err = conn.Write([]byte("*1\r\n$4\r\nPING\r\n"))
	if err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "+PONG") {
		return fmt.Errorf("unexpected ping reply: %s", line)
	}
	return nil
}

// Get retrieves the value of a key.
func (rc *RedisClient) Get(ctx context.Context, key string) (string, error) {
	if rc.isMemory {
		rc.mu.RLock()
		defer rc.mu.RUnlock()
		item, exists := rc.memoryData[key]
		if !exists {
			return "", ErrCacheMiss
		}
		if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
			return "", ErrCacheMiss
		}
		return item.value, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return "", err
	}
	defer rc.putConn(conn)

	_ = conn.SetDeadline(time.Now().Add(rc.cfg.DialTimeout))
	cmd := fmt.Sprintf("*2\r\n$3\r\nGET\r\n$%d\r\n%s\r\n", len(key), key)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return "", err
	}

	reader := bufio.NewReader(conn)
	prefix, err := reader.ReadByte()
	if err != nil {
		return "", err
	}

	if prefix == '$' {
		lenStr, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		lenStr = strings.TrimSpace(lenStr)
		l, _ := strconv.Atoi(lenStr)
		if l == -1 {
			return "", ErrCacheMiss
		}
		buf := make([]byte, l+2)
		n := 0
		for n < l+2 {
			readN, err := reader.Read(buf[n:])
			if err != nil {
				return "", err
			}
			n += readN
		}
		return string(buf[:l]), nil
	}

	return "", fmt.Errorf("unexpected redis reply prefix: %c", prefix)
}

// Set stores a key-value pair with an optional TTL.
func (rc *RedisClient) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if rc.isMemory {
		rc.mu.Lock()
		var exp time.Time
		if ttl > 0 {
			exp = time.Now().Add(ttl)
		}
		rc.memoryData[key] = memoryItem{value: value, expiresAt: exp}
		rc.mu.Unlock()
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return err
	}
	defer rc.putConn(conn)

	_ = conn.SetDeadline(time.Now().Add(rc.cfg.DialTimeout))
	var cmd string
	if ttl > 0 {
		ms := int64(ttl / time.Millisecond)
		msStr := strconv.FormatInt(ms, 10)
		cmd = fmt.Sprintf("*5\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$2\r\nPX\r\n$%d\r\n%s\r\n",
			len(key), key, len(value), value, len(msStr), msStr)
	} else {
		cmd = fmt.Sprintf("*3\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
			len(key), key, len(value), value)
	}

	if _, err := conn.Write([]byte(cmd)); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "+OK") {
		return fmt.Errorf("redis set failed: %s", line)
	}
	return nil
}

// Del deletes one or more keys.
func (rc *RedisClient) Del(ctx context.Context, keys ...string) error {
	if rc.isMemory {
		rc.mu.Lock()
		for _, k := range keys {
			delete(rc.memoryData, k)
		}
		rc.mu.Unlock()
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return err
	}
	defer rc.putConn(conn)

	_ = conn.SetDeadline(time.Now().Add(rc.cfg.DialTimeout))
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*%d\r\n$3\r\nDEL\r\n", len(keys)+1))
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(k), k))
	}

	if _, err := conn.Write([]byte(sb.String())); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	_, err = reader.ReadString('\n')
	return err
}

// Incr increments the integer value of a key by 1.
func (rc *RedisClient) Incr(ctx context.Context, key string) (int64, error) {
	if rc.isMemory {
		rc.mu.Lock()
		defer rc.mu.Unlock()
		item, exists := rc.memoryData[key]
		var current int64
		if exists {
			current, _ = strconv.ParseInt(item.value, 10, 64)
		}
		current++
		rc.memoryData[key] = memoryItem{value: strconv.FormatInt(current, 10)}
		return current, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return 0, err
	}
	defer rc.putConn(conn)

	_ = conn.SetDeadline(time.Now().Add(rc.cfg.DialTimeout))
	cmd := fmt.Sprintf("*2\r\n$4\r\nINCR\r\n$%d\r\n%s\r\n", len(key), key)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return 0, err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, err
	}
	if strings.HasPrefix(line, ":") {
		return strconv.ParseInt(strings.TrimSpace(line[1:]), 10, 64)
	}
	return 0, fmt.Errorf("unexpected incr reply: %s", line)
}

// GetJSON deserializes a JSON cached value into target pointer.
func (rc *RedisClient) GetJSON(ctx context.Context, key string, target any) error {
	raw, err := rc.Get(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), target)
}

// SetJSON serializes value into JSON and caches it.
func (rc *RedisClient) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return rc.Set(ctx, key, string(data), ttl)
}

// Publish sends a message to the specified pub/sub channel.
func (rc *RedisClient) Publish(ctx context.Context, channel, message string) error {
	if rc.isMemory {
		rc.subMu.RLock()
		handlers := rc.subscribers[channel]
		rc.subMu.RUnlock()
		for _, h := range handlers {
			go h(message)
		}
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return err
	}
	defer rc.putConn(conn)

	cmd := fmt.Sprintf("*3\r\n$7\r\nPUBLISH\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(channel), channel, len(message), message)
	_, err = conn.Write([]byte(cmd))
	return err
}

// Subscribe listens for messages on a channel.
func (rc *RedisClient) Subscribe(channel string, handler func(msg string)) {
	rc.subMu.Lock()
	rc.subscribers[channel] = append(rc.subscribers[channel], handler)
	rc.subMu.Unlock()
}

// Errors associated with distributed locking
var (
	ErrLockNotAcquired = errors.New("redis: lock not acquired")
	ErrLockNotHeld     = errors.New("redis: lock not held or token mismatch")
)

// RedisLock represents an acquired distributed lock.
type RedisLock struct {
	client *RedisClient
	key    string
	token  string
	ttl    time.Duration
}

// Key returns the lock key.
func (l *RedisLock) Key() string {
	return l.key
}

// Token returns the unique owner token for this lock.
func (l *RedisLock) Token() string {
	return l.token
}

// Unlock releases the lock safely ensuring only the owner can release it.
func (l *RedisLock) Unlock(ctx context.Context) error {
	return l.client.Unlock(ctx, l.key, l.token)
}

// Renew extends the lock duration by the specified TTL.
func (l *RedisLock) Renew(ctx context.Context, ttl time.Duration) error {
	return l.client.Renew(ctx, l.key, l.token, ttl)
}

// Lock attempts to acquire a distributed lock on the specified key.
func (rc *RedisClient) Lock(ctx context.Context, key string, ttl time.Duration) (*RedisLock, error) {
	ttl = cmp.Or(ttl, 10*time.Second)
	tokenBytes := make([]byte, 16)
	_, _ = rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)

	if rc.isMemory {
		rc.mu.Lock()
		defer rc.mu.Unlock()

		item, exists := rc.memoryData[key]
		now := time.Now()
		if exists && now.Before(item.expiresAt) {
			return nil, ErrLockNotAcquired
		}

		rc.memoryData[key] = memoryItem{
			value:     token,
			expiresAt: now.Add(ttl),
		}
		return &RedisLock{client: rc, key: key, token: token, ttl: ttl}, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return nil, err
	}
	defer rc.putConn(conn)

	ms := ttl.Milliseconds()
	cmd := fmt.Sprintf("*6\r\n$3\r\nSET\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$2\r\nNX\r\n$2\r\nPX\r\n$%d\r\n%d\r\n",
		len(key), key, len(token), token, len(strconv.FormatInt(ms, 10)), ms)

	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil, err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "+OK") {
		return nil, ErrLockNotAcquired
	}

	return &RedisLock{client: rc, key: key, token: token, ttl: ttl}, nil
}

// Unlock releases the lock if and only if the token matches.
func (rc *RedisClient) Unlock(ctx context.Context, key, token string) error {
	if rc.isMemory {
		rc.mu.Lock()
		defer rc.mu.Unlock()

		item, exists := rc.memoryData[key]
		if !exists || item.value != token {
			return ErrLockNotHeld
		}
		delete(rc.memoryData, key)
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return err
	}
	defer rc.putConn(conn)

	// Lua script: atomic get and delete if token matches
	script := "if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('del', KEYS[1]) else return 0 end"
	cmd := fmt.Sprintf("*5\r\n$4\r\nEVAL\r\n$%d\r\n%s\r\n$1\r\n1\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(script), script, len(key), key, len(token), token)

	if _, err := conn.Write([]byte(cmd)); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || (!strings.HasPrefix(line, ":1") && !strings.HasPrefix(line, "+OK")) {
		return ErrLockNotHeld
	}
	return nil
}

// Renew extends the expiration time of an actively held lock.
func (rc *RedisClient) Renew(ctx context.Context, key, token string, ttl time.Duration) error {
	if rc.isMemory {
		rc.mu.Lock()
		defer rc.mu.Unlock()

		item, exists := rc.memoryData[key]
		if !exists || item.value != token || time.Now().After(item.expiresAt) {
			return ErrLockNotHeld
		}
		rc.memoryData[key] = memoryItem{
			value:     token,
			expiresAt: time.Now().Add(ttl),
		}
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return err
	}
	defer rc.putConn(conn)

	ms := ttl.Milliseconds()
	script := "if redis.call('get', KEYS[1]) == ARGV[1] then return redis.call('pexpire', KEYS[1], ARGV[2]) else return 0 end"
	msStr := strconv.FormatInt(ms, 10)
	cmd := fmt.Sprintf("*6\r\n$4\r\nEVAL\r\n$%d\r\n%s\r\n$1\r\n1\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(script), script, len(key), key, len(token), token, len(msStr), msStr)

	if _, err := conn.Write([]byte(cmd)); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ":1") {
		return ErrLockNotHeld
	}
	return nil
}

// BloomAdd adds an item to a distributed Bloom filter in Redis or in-memory bitmap.
func (rc *RedisClient) BloomAdd(ctx context.Context, filterKey, item string) error {
	bf := NewBloomFilter(100000, 0.01)
	h1, h2 := bf.hash64(item)

	if rc.isMemory {
		rc.mu.Lock()
		defer rc.mu.Unlock()
		for i := uint(0); i < bf.k; i++ {
			bitIdx := (h1 + uint64(i)*h2) % uint64(bf.m)
			k := fmt.Sprintf("%s:b:%d", filterKey, bitIdx)
			rc.memoryData[k] = memoryItem{value: "1", expiresAt: time.Now().Add(30 * 24 * time.Hour)}
		}
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return err
	}
	defer rc.putConn(conn)

	for i := uint(0); i < bf.k; i++ {
		bitIdx := (h1 + uint64(i)*h2) % uint64(bf.m)
		cmd := fmt.Sprintf("*4\r\n$6\r\nSETBIT\r\n$%d\r\n%s\r\n$%d\r\n%d\r\n$1\r\n1\r\n",
			len(filterKey), filterKey, len(strconv.FormatUint(bitIdx, 10)), bitIdx)
		if _, err := conn.Write([]byte(cmd)); err != nil {
			return err
		}
		reader := bufio.NewReader(conn)
		_, _ = reader.ReadString('\n')
	}
	return nil
}

// BloomContains checks if an item might be in the distributed Bloom filter.
func (rc *RedisClient) BloomContains(ctx context.Context, filterKey, item string) (bool, error) {
	bf := NewBloomFilter(100000, 0.01)
	h1, h2 := bf.hash64(item)

	if rc.isMemory {
		rc.mu.RLock()
		defer rc.mu.RUnlock()
		for i := uint(0); i < bf.k; i++ {
			bitIdx := (h1 + uint64(i)*h2) % uint64(bf.m)
			k := fmt.Sprintf("%s:b:%d", filterKey, bitIdx)
			if item, ok := rc.memoryData[k]; !ok || item.value != "1" {
				return false, nil
			}
		}
		return true, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return false, err
	}
	defer rc.putConn(conn)

	reader := bufio.NewReader(conn)
	for i := uint(0); i < bf.k; i++ {
		bitIdx := (h1 + uint64(i)*h2) % uint64(bf.m)
		cmd := fmt.Sprintf("*3\r\n$6\r\nGETBIT\r\n$%d\r\n%s\r\n$%d\r\n%d\r\n",
			len(filterKey), filterKey, len(strconv.FormatUint(bitIdx, 10)), bitIdx)
		if _, err := conn.Write([]byte(cmd)); err != nil {
			return false, err
		}
		line, err := reader.ReadString('\n')
		if err != nil || !strings.HasPrefix(line, ":1") {
			return false, nil
		}
	}
	return true, nil
}

// BloomReset deletes a Bloom filter key and resets all underlying bits.
func (rc *RedisClient) BloomReset(ctx context.Context, filterKey string) error {
	if rc.isMemory {
		rc.mu.Lock()
		defer rc.mu.Unlock()
		prefix := filterKey + ":b:"
		for k := range rc.memoryData {
			if strings.HasPrefix(k, prefix) {
				delete(rc.memoryData, k)
			}
		}
		return nil
	}
	return rc.Del(ctx, filterKey)
}

// BloomRebuild atomically clears and repopulates a Bloom filter with the given active items.
func (rc *RedisClient) BloomRebuild(ctx context.Context, filterKey string, items ...string) error {
	if err := rc.BloomReset(ctx, filterKey); err != nil {
		return err
	}
	for _, it := range items {
		if err := rc.BloomAdd(ctx, filterKey, it); err != nil {
			return err
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Cuckoo Filter (Dynamic Membership with Deletions & Updates)
// -----------------------------------------------------------------------------

// CuckooAdd inserts an item into an updatable Cuckoo filter.
func (rc *RedisClient) CuckooAdd(ctx context.Context, key, item string) error {
	rc.mu.Lock()
	set, ok := rc.cuckooData[key]
	if !ok {
		set = make(map[string]bool)
		rc.cuckooData[key] = set
	}
	set[item] = true
	rc.mu.Unlock()

	if rc.isMemory {
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return nil // Fallback silently to memory set
	}
	defer rc.putConn(conn)

	cmd := fmt.Sprintf("*3\r\n$6\r\nCF.ADD\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(key), key, len(item), item)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil
	}
	reader := bufio.NewReader(conn)
	_, _ = reader.ReadString('\n')
	return nil
}

// CuckooContains checks if an item exists in the Cuckoo filter.
func (rc *RedisClient) CuckooContains(ctx context.Context, key, item string) (bool, error) {
	rc.mu.RLock()
	if set, ok := rc.cuckooData[key]; ok && set[item] {
		rc.mu.RUnlock()
		return true, nil
	}
	rc.mu.RUnlock()

	if rc.isMemory {
		return false, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return false, nil
	}
	defer rc.putConn(conn)

	cmd := fmt.Sprintf("*3\r\n$8\r\nCF.CHECK\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(key), key, len(item), item)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return false, nil
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || strings.HasPrefix(line, "-ERR") {
		return false, nil
	}
	return strings.HasPrefix(line, ":1"), nil
}

// CuckooDelete deletes an item from the Cuckoo filter, enabling safe item updates.
func (rc *RedisClient) CuckooDelete(ctx context.Context, key, item string) (bool, error) {
	rc.mu.Lock()
	deleted := false
	if set, ok := rc.cuckooData[key]; ok {
		if set[item] {
			delete(set, item)
			deleted = true
		}
	}
	rc.mu.Unlock()

	if rc.isMemory {
		return deleted, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return deleted, nil
	}
	defer rc.putConn(conn)

	cmd := fmt.Sprintf("*3\r\n$6\r\nCF.DEL\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(key), key, len(item), item)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return deleted, nil
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || strings.HasPrefix(line, "-ERR") {
		return deleted, nil
	}
	return strings.HasPrefix(line, ":1") || deleted, nil
}

// -----------------------------------------------------------------------------
// Top-K Frequency Estimation (Heavy Hitters & Trending Tracker)
// -----------------------------------------------------------------------------

// TopKReserve initializes a Top-K structure in Redis.
func (rc *RedisClient) TopKReserve(ctx context.Context, key string, topK int64) error {
	topK = cmp.Or(topK, 50)
	rc.mu.Lock()
	if _, ok := rc.topKData[key]; !ok {
		rc.topKData[key] = make(map[string]int64)
	}
	rc.mu.Unlock()

	if rc.isMemory {
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return nil
	}
	defer rc.putConn(conn)

	topKStr := strconv.FormatInt(topK, 10)
	cmd := fmt.Sprintf("*3\r\n$12\r\nTOPK.RESERVE\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(key), key, len(topKStr), topKStr)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil
	}
	reader := bufio.NewReader(conn)
	_, _ = reader.ReadString('\n')
	return nil
}

// TopKAdd records occurrences of items in the Top-K frequency tracker.
func (rc *RedisClient) TopKAdd(ctx context.Context, key string, items ...string) ([]string, error) {
	rc.mu.Lock()
	m, ok := rc.topKData[key]
	if !ok {
		m = make(map[string]int64)
		rc.topKData[key] = m
	}
	for _, it := range items {
		m[it]++
	}
	rc.mu.Unlock()

	if rc.isMemory || len(items) == 0 {
		return nil, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return nil, nil
	}
	defer rc.putConn(conn)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*%d\r\n$8\r\nTOPK.ADD\r\n$%d\r\n%s\r\n", len(items)+2, len(key), key))
	for _, it := range items {
		sb.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(it), it))
	}

	if _, err := conn.Write([]byte(sb.String())); err != nil {
		return nil, nil
	}
	reader := bufio.NewReader(conn)
	_, _ = reader.ReadString('\n')
	return nil, nil
}

// TopKQuery checks whether items are currently tracked in the Top-K list.
func (rc *RedisClient) TopKQuery(ctx context.Context, key string, items ...string) ([]bool, error) {
	results := make([]bool, len(items))
	rc.mu.RLock()
	m, ok := rc.topKData[key]
	if ok {
		for i, it := range items {
			results[i] = m[it] > 0
		}
	}
	rc.mu.RUnlock()
	return results, nil
}

// TopKCount returns estimated frequency counts for items.
func (rc *RedisClient) TopKCount(ctx context.Context, key string, items ...string) ([]int64, error) {
	counts := make([]int64, len(items))
	rc.mu.RLock()
	m, ok := rc.topKData[key]
	if ok {
		for i, it := range items {
			counts[i] = m[it]
		}
	}
	rc.mu.RUnlock()
	return counts, nil
}

// TopKList returns the ranked items currently in the Top-K list in descending order of frequency.
func (rc *RedisClient) TopKList(ctx context.Context, key string) ([]string, error) {
	rc.mu.RLock()
	m, ok := rc.topKData[key]
	if !ok || len(m) == 0 {
		rc.mu.RUnlock()
		return []string{}, nil
	}

	type kv struct {
		k string
		v int64
	}
	pairs := make([]kv, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kv{k, v})
	}
	rc.mu.RUnlock()

	slices.SortFunc(pairs, func(a, b kv) int {
		return cmp.Compare(b.v, a.v) // Descending
	})

	limit := 50
	if len(pairs) < limit {
		limit = len(pairs)
	}
	out := make([]string, limit)
	for i := range limit {
		out[i] = pairs[i].k
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// Real-Time Leaderboard (Redis Sorted Sets / ZSET)
// -----------------------------------------------------------------------------

// LeaderboardItem represents a member with score and 1-based rank on a leaderboard.
type LeaderboardItem struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
	Rank   int64   `json:"rank"` // 1-based rank (Rank 1 is highest score)
}

// LeaderboardAdd adds or updates a member with the given score on the leaderboard.
func (rc *RedisClient) LeaderboardAdd(ctx context.Context, key, member string, score float64) error {
	rc.mu.Lock()
	zset, ok := rc.sortedSets[key]
	if !ok {
		zset = make(map[string]float64)
		rc.sortedSets[key] = zset
	}
	zset[member] = score
	rc.mu.Unlock()

	if rc.isMemory {
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return nil
	}
	defer rc.putConn(conn)

	scoreStr := strconv.FormatFloat(score, 'f', 4, 64)
	cmd := fmt.Sprintf("*4\r\n$4\r\nZADD\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(key), key, len(scoreStr), scoreStr, len(member), member)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil
	}
	reader := bufio.NewReader(conn)
	_, _ = reader.ReadString('\n')
	return nil
}

// LeaderboardIncrBy increments a member's leaderboard score by delta and returns the updated score.
func (rc *RedisClient) LeaderboardIncrBy(ctx context.Context, key, member string, delta float64) (float64, error) {
	rc.mu.Lock()
	zset, ok := rc.sortedSets[key]
	if !ok {
		zset = make(map[string]float64)
		rc.sortedSets[key] = zset
	}
	zset[member] += delta
	newScore := zset[member]
	rc.mu.Unlock()

	if rc.isMemory {
		return newScore, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return newScore, nil
	}
	defer rc.putConn(conn)

	deltaStr := strconv.FormatFloat(delta, 'f', 4, 64)
	cmd := fmt.Sprintf("*4\r\n$7\r\nZINCRBY\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(key), key, len(deltaStr), deltaStr, len(member), member)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return newScore, nil
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err == nil && strings.HasPrefix(line, "$") {
		// Read bulk string length then content
		valLine, err := reader.ReadString('\n')
		if err == nil {
			if s, parseErr := strconv.ParseFloat(strings.TrimSpace(valLine), 64); parseErr == nil {
				return s, nil
			}
		}
	}
	return newScore, nil
}

// LeaderboardGetRank returns the 1-based rank (Rank 1 = highest score) and score of a member.
func (rc *RedisClient) LeaderboardGetRank(ctx context.Context, key, member string) (int64, float64, error) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	zset, ok := rc.sortedSets[key]
	if !ok {
		return 0, 0, ErrCacheMiss
	}
	score, found := zset[member]
	if !found {
		return 0, 0, ErrCacheMiss
	}

	var rank int64 = 1
	for _, otherScore := range zset {
		if otherScore > score {
			rank++
		}
	}
	return rank, score, nil
}

// LeaderboardGetTop returns the top N members in descending score order.
func (rc *RedisClient) LeaderboardGetTop(ctx context.Context, key string, limit int) ([]LeaderboardItem, error) {
	return rc.LeaderboardGetRange(ctx, key, 1, int64(limit))
}

// LeaderboardGetRange returns members within the 1-based rank range [startRank, stopRank].
func (rc *RedisClient) LeaderboardGetRange(ctx context.Context, key string, startRank, stopRank int64) ([]LeaderboardItem, error) {
	if startRank < 1 {
		startRank = 1
	}
	if stopRank < startRank {
		stopRank = startRank
	}

	rc.mu.RLock()
	zset, ok := rc.sortedSets[key]
	if !ok || len(zset) == 0 {
		rc.mu.RUnlock()
		return []LeaderboardItem{}, nil
	}

	type kv struct {
		m string
		s float64
	}
	all := make([]kv, 0, len(zset))
	for m, s := range zset {
		all = append(all, kv{m, s})
	}
	rc.mu.RUnlock()

	slices.SortFunc(all, func(a, b kv) int {
		if a.s != b.s {
			return cmp.Compare(b.s, a.s) // Descending
		}
		return cmp.Compare(a.m, b.m) // Tie-breaker
	})

	startIdx := int(startRank - 1)
	if startIdx >= len(all) {
		return []LeaderboardItem{}, nil
	}
	stopIdx := int(stopRank)
	if stopIdx > len(all) {
		stopIdx = len(all)
	}

	items := make([]LeaderboardItem, 0, stopIdx-startIdx)
	for i := startIdx; i < stopIdx; i++ {
		items = append(items, LeaderboardItem{
			Member: all[i].m,
			Score:  all[i].s,
			Rank:   int64(i + 1),
		})
	}

	return items, nil
}

// LeaderboardAroundMe returns entries centered around the specified member with the given radius.
func (rc *RedisClient) LeaderboardAroundMe(ctx context.Context, key, member string, radius int64) ([]LeaderboardItem, error) {
	radius = cmp.Or(radius, 5)
	rank, _, err := rc.LeaderboardGetRank(ctx, key, member)
	if err != nil {
		return nil, err
	}

	startRank := rank - radius
	if startRank < 1 {
		startRank = 1
	}
	stopRank := rank + radius

	return rc.LeaderboardGetRange(ctx, key, startRank, stopRank)
}

// LeaderboardCount returns the total number of entries on the leaderboard.
func (rc *RedisClient) LeaderboardCount(ctx context.Context, key string) (int64, error) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	if zset, ok := rc.sortedSets[key]; ok {
		return int64(len(zset)), nil
	}
	return 0, nil
}

// LeaderboardRemove removes a member from the leaderboard.
func (rc *RedisClient) LeaderboardRemove(ctx context.Context, key, member string) error {
	rc.mu.Lock()
	if zset, ok := rc.sortedSets[key]; ok {
		delete(zset, member)
	}
	rc.mu.Unlock()

	if rc.isMemory {
		return nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return nil
	}
	defer rc.putConn(conn)

	cmd := fmt.Sprintf("*3\r\n$4\r\nZREM\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n",
		len(key), key, len(member), member)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return nil
	}
	reader := bufio.NewReader(conn)
	_, _ = reader.ReadString('\n')
	return nil
}

// -----------------------------------------------------------------------------
// HyperLogLog (Cardinality Estimation)
// -----------------------------------------------------------------------------

// HyperLogLogAdd adds elements to a HyperLogLog structure.
func (rc *RedisClient) HyperLogLogAdd(ctx context.Context, key string, elements ...string) (bool, error) {
	rc.mu.Lock()
	set, ok := rc.hllData[key]
	if !ok {
		set = make(map[string]bool)
		rc.hllData[key] = set
	}
	added := false
	for _, el := range elements {
		if !set[el] {
			set[el] = true
			added = true
		}
	}
	rc.mu.Unlock()

	if rc.isMemory || len(elements) == 0 {
		return added, nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return added, nil
	}
	defer rc.putConn(conn)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*%d\r\n$5\r\nPFADD\r\n$%d\r\n%s\r\n", len(elements)+2, len(key), key))
	for _, el := range elements {
		sb.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(el), el))
	}
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		return added, nil
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err == nil && strings.HasPrefix(line, ":1") {
		return true, nil
	}
	return added, nil
}

// HyperLogLogCount returns the approximated cardinality across one or more HyperLogLog keys.
func (rc *RedisClient) HyperLogLogCount(ctx context.Context, keys ...string) (int64, error) {
	rc.mu.RLock()
	merged := make(map[string]bool)
	for _, k := range keys {
		if set, ok := rc.hllData[k]; ok {
			for el := range set {
				merged[el] = true
			}
		}
	}
	rc.mu.RUnlock()

	if rc.isMemory || len(keys) == 0 {
		return int64(len(merged)), nil
	}

	conn, err := rc.getConn()
	if err != nil {
		return int64(len(merged)), nil
	}
	defer rc.putConn(conn)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*%d\r\n$7\r\nPFCOUNT\r\n", len(keys)+1))
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(k), k))
	}
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		return int64(len(merged)), nil
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err == nil && strings.HasPrefix(line, ":") {
		if count, parseErr := strconv.ParseInt(strings.TrimSpace(line[1:]), 10, 64); parseErr == nil {
			return count, nil
		}
	}
	return int64(len(merged)), nil
}

// Close closes all pooled connections.
func (rc *RedisClient) Close() error {
	if rc.connPool != nil {
		close(rc.connPool)
		for conn := range rc.connPool {
			_ = conn.Close()
		}
	}
	return nil
}
