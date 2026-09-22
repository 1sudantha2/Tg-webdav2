package storage

import (
    "container/list"
    "sync"
    "time"
)

// ChunkKey identifies a specific chunk of a file
type ChunkKey struct {
    FileID string
    Chunk  int64
}

// CacheEntry holds cached chunk data
type CacheEntry struct {
    Key       ChunkKey
    Data      []byte
    Size      int64
    CreatedAt time.Time
    element   *list.Element
}

// ChunkCache is an LRU cache for file chunks
type ChunkCache struct {
    mu       sync.RWMutex
    maxBytes int64
    curBytes int64
    items    map[ChunkKey]*CacheEntry
    lru      *list.List
}

func NewChunkCache(maxMB int64) *ChunkCache {
    return &ChunkCache{
        maxBytes: maxMB * 1024 * 1024,
        items:    make(map[ChunkKey]*CacheEntry),
        lru:      list.New(),
    }
}

func (c *ChunkCache) Get(key ChunkKey) ([]byte, bool) {
    c.mu.RLock()
    entry, ok := c.items[key]
    c.mu.RUnlock()

    if !ok {
        return nil, false
    }

    c.mu.Lock()
    c.lru.MoveToFront(entry.element)
    c.mu.Unlock()

    return entry.Data, true
}

func (c *ChunkCache) Put(key ChunkKey, data []byte) {
    c.mu.Lock()
    defer c.mu.Unlock()

    // If already exists, remove old
    if entry, ok := c.items[key]; ok {
        c.lru.Remove(entry.element)
        c.curBytes -= entry.Size
        delete(c.items, key)
    }

    // Evict until there's space
    size := int64(len(data))
    for c.curBytes+size > c.maxBytes && c.lru.Len() > 0 {
        oldest := c.lru.Back()
        if oldest == nil {
            break
        }
        entry := oldest.Value.(*CacheEntry)
        c.lru.Remove(oldest)
        c.curBytes -= entry.Size
        delete(c.items, entry.Key)
    }

    entry := &CacheEntry{
        Key:       key,
        Data:      data,
        Size:      size,
        CreatedAt: time.Now(),
    }
    entry.element = c.lru.PushFront(entry)
    c.items[key] = entry
    c.curBytes += size
}

func (c *ChunkCache) Stats() (int, int64, int64) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return len(c.items), c.curBytes, c.maxBytes
}
