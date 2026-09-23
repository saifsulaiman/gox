package goweb

import (
	"hash/fnv"
	"math"
	"sync"
)

// BloomFilter is a thread-safe, high-performance bitset Bloom Filter for
// defending against cache penetration (rejecting non-existent queries before hitting DB).
type BloomFilter struct {
	mu     sync.RWMutex
	m      uint     // Number of bits
	k      uint     // Number of hash functions
	bits   []uint64 // Bitset storage
	count  uint64   // Number of items added
}

// NewBloomFilter creates a Bloom Filter tuned for expectedItems and desired false positive rate (e.g., 0.01 for 1%).
func NewBloomFilter(expectedItems uint, falsePositiveRate float64) *BloomFilter {
	if expectedItems == 0 {
		expectedItems = 1000
	}
	if falsePositiveRate <= 0 || falsePositiveRate >= 1 {
		falsePositiveRate = 0.01 // 1% default
	}

	// m = - (n * ln(p)) / (ln(2)^2)
	ln2 := math.Ln2
	mFloat := -float64(expectedItems) * math.Log(falsePositiveRate) / (ln2 * ln2)
	m := uint(math.Ceil(mFloat))
	if m < 64 {
		m = 64
	}

	// k = (m / n) * ln(2)
	kFloat := (float64(m) / float64(expectedItems)) * ln2
	k := uint(math.Round(kFloat))
	if k < 1 {
		k = 1
	}

	words := (m + 63) / 64
	return &BloomFilter{
		m:    m,
		k:    k,
		bits: make([]uint64, words),
	}
}

// hash64 computes two 64-bit hashes using FNV-1a variations for Kirsch-Mitzenmacher double-hashing.
func (bf *BloomFilter) hash64(item string) (uint64, uint64) {
	h1 := fnv.New64a()
	_, _ = h1.Write([]byte(item))
	val1 := h1.Sum64()

	// Secondary hash with salt
	h2 := fnv.New64()
	_, _ = h2.Write([]byte(item))
	_, _ = h2.Write([]byte{0x5f, 0x37, 0x59, 0xdf}) // golden ratio salt
	val2 := h2.Sum64()
	if val2 == 0 {
		val2 = 1
	}

	return val1, val2
}

// Add inserts an item into the Bloom filter.
func (bf *BloomFilter) Add(item string) {
	h1, h2 := bf.hash64(item)
	bf.mu.Lock()
	defer bf.mu.Unlock()

	for i := uint(0); i < bf.k; i++ {
		bitIdx := (h1 + uint64(i)*h2) % uint64(bf.m)
		wordIdx := bitIdx / 64
		bitOffset := bitIdx % 64
		bf.bits[wordIdx] |= (1 << bitOffset)
	}
	bf.count++
}

// Contains tests if an item might be in the set (true) or is definitely not in the set (false).
func (bf *BloomFilter) Contains(item string) bool {
	h1, h2 := bf.hash64(item)
	bf.mu.RLock()
	defer bf.mu.RUnlock()

	for i := uint(0); i < bf.k; i++ {
		bitIdx := (h1 + uint64(i)*h2) % uint64(bf.m)
		wordIdx := bitIdx / 64
		bitOffset := bitIdx % 64
		if (bf.bits[wordIdx] & (1 << bitOffset)) == 0 {
			return false // Definitely not in set
		}
	}
	return true // Maybe in set
}

// Count returns the number of items added to the filter.
func (bf *BloomFilter) Count() uint64 {
	bf.mu.RLock()
	defer bf.mu.RUnlock()
	return bf.count
}

// Reset clears all bits in the Bloom filter.
func (bf *BloomFilter) Reset() {
	bf.mu.Lock()
	defer bf.mu.Unlock()
	clear(bf.bits)
	bf.count = 0
}
