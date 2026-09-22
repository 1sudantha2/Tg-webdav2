package telegram

import (
    "context"
    "fmt"
    "io"
    "sync"
    "time"

    "github.com/gotd/td/tg"
    "github.com/yourusername/telegram-webdav/internal/config"
    "github.com/yourusername/telegram-webdav/internal/storage"
)

const (
    defaultChunkSize = 512 * 1024 // 512KB chunks
    maxRetries       = 3
    retryDelay       = 500 * time.Millisecond
)

// TelegramReader implements io.ReadSeeker for Telegram files
type TelegramReader struct {
    ctx      context.Context
    client   *Client
    location tg.InputFileLocationClass
    size     int64
    offset   int64
    cfg      *config.Config

    // Prefetch
    prefetchMu    sync.Mutex
    prefetchCache map[int64][]byte
    prefetching   map[int64]bool
}

func NewTelegramReader(
    ctx context.Context,
    client *Client,
    location tg.InputFileLocationClass,
    size int64,
    cfg *config.Config,
) *TelegramReader {
    return &TelegramReader{
        ctx:           ctx,
        client:        client,
        location:      location,
        size:          size,
        offset:        0,
        cfg:           cfg,
        prefetchCache: make(map[int64][]byte),
        prefetching:   make(map[int64]bool),
    }
}

func (r *TelegramReader) Read(p []byte) (int, error) {
    if r.offset >= r.size {
        return 0, io.EOF
    }

    chunkSize := int64(defaultChunkSize)
    chunkIndex := r.offset / chunkSize
    chunkStart := chunkIndex * chunkSize
    intraOffset := r.offset - chunkStart

    // Try prefetch cache first
    data, err := r.getChunk(chunkIndex, chunkStart, chunkSize)
    if err != nil {
        return 0, fmt.Errorf("get chunk %d: %w", chunkIndex, err)
    }

    // Slice data from intra-offset
    available := data[intraOffset:]
    n := copy(p, available)
    r.offset += int64(n)

    // Trigger prefetch for next chunks
    go r.prefetchNext(chunkIndex, chunkSize)

    return n, nil
}

func (r *TelegramReader) Seek(offset int64, whence int) (int64, error) {
    var newOffset int64
    switch whence {
    case io.SeekStart:
        newOffset = offset
    case io.SeekCurrent:
        newOffset = r.offset + offset
    case io.SeekEnd:
        newOffset = r.size + offset
    default:
        return 0, fmt.Errorf("invalid whence: %d", whence)
    }

    if newOffset < 0 {
        return 0, fmt.Errorf("negative seek position")
    }
    if newOffset > r.size {
        newOffset = r.size
    }

    r.offset = newOffset
    return r.offset, nil
}

func (r *TelegramReader) getChunk(chunkIndex, chunkStart, chunkSize int64) ([]byte, error) {
    // Check local prefetch cache
    r.prefetchMu.Lock()
    if data, ok := r.prefetchCache[chunkIndex]; ok {
        r.prefetchMu.Unlock()
        return data, nil
    }
    r.prefetchMu.Unlock()

    // Check global LRU cache
    cacheKey := storage.ChunkKey{
        FileID: fmt.Sprintf("%v", r.location),
        Chunk:  chunkIndex,
    }
    if data, ok := r.client.cache.Get(cacheKey); ok {
        return data, nil
    }

    // Download with retry
    var data []byte
    var lastErr error
    
    limit := chunkSize
    if chunkStart+limit > r.size {
        limit = r.size - chunkStart
    }

    for attempt := 0; attempt < maxRetries; attempt++ {
        if attempt > 0 {
            time.Sleep(retryDelay * time.Duration(attempt))
        }
        
        chunk, err := r.client.DownloadChunk(r.ctx, r.location, chunkStart, limit)
        if err != nil {
            lastErr = err
            continue
        }
        data = chunk
        lastErr = nil
        break
    }

    if lastErr != nil {
        return nil, lastErr
    }

    return data, nil
}

func (r *TelegramReader) prefetchNext(currentChunk, chunkSize int64) {
    for i := int64(1); i <= int64(r.cfg.PrefetchChunks); i++ {
        nextChunk := currentChunk + i
        nextStart := nextChunk * chunkSize

        if nextStart >= r.size {
            break
        }

        r.prefetchMu.Lock()
        if r.prefetching[nextChunk] {
            r.prefetchMu.Unlock()
            continue
        }
        r.prefetching[nextChunk] = true
        r.prefetchMu.Unlock()

        go func(idx, start int64) {
            defer func() {
                r.prefetchMu.Lock()
                delete(r.prefetching, idx)
                r.prefetchMu.Unlock()
            }()

            limit := chunkSize
            if start+limit > r.size {
                limit = r.size - start
            }

            ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
            defer cancel()

            data, err := r.client.DownloadChunk(ctx, r.location, start, limit)
            if err != nil {
                return
            }

            r.prefetchMu.Lock()
            r.prefetchCache[idx] = data
            // Keep only recent prefetch chunks in local cache
            if len(r.prefetchCache) > 10 {
                // Remove oldest
                var minKey int64 = 1<<62
                for k := range r.prefetchCache {
                    if k < minKey {
                        minKey = k
                    }
                }
                delete(r.prefetchCache, minKey)
            }
            r.prefetchMu.Unlock()
        }(nextChunk, nextStart)
    }
}
