package webdav

import (
    "net/http"
    "strings"

    "golang.org/x/net/webdav"
    "github.com/yourusername/telegram-webdav/internal/config"
    "github.com/yourusername/telegram-webdav/internal/storage"
    "github.com/yourusername/telegram-webdav/internal/telegram"
)

type Handler struct {
    webdavHandler *webdav.Handler
    fs            *TelegramFS
}

func NewHandler(db *storage.DB, client *telegram.Client, cfg *config.Config) *Handler {
    tfs := NewTelegramFS(db, client, cfg)

    wdHandler := &webdav.Handler{
        Prefix:     "/dav",
        FileSystem: tfs,
        LockSystem: webdav.NewMemLS(),
        Logger: func(r *http.Request, err error) {
            if err != nil && !isIgnorableError(err) {
                // Log only significant errors
            }
        },
    }

    return &Handler{
        webdavHandler: wdHandler,
        fs:            tfs,
    }
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // Set CORS headers for clients
    w.Header().Set("DAV", "1, 2")
    w.Header().Set("Allow", "OPTIONS, GET, HEAD, POST, PUT, DELETE, PROPFIND, PROPPATCH, MKCOL, COPY, MOVE, LOCK, UNLOCK")
    w.Header().Set("MS-Author-Via", "DAV")

    // Handle preflight
    if r.Method == "OPTIONS" {
        w.WriteHeader(http.StatusOK)
        return
    }

    // Add range request support headers for video seeking
    if r.Method == "GET" || r.Method == "HEAD" {
        w.Header().Set("Accept-Ranges", "bytes")
    }

    h.webdavHandler.ServeHTTP(w, r)
}

func isIgnorableError(err error) bool {
    msg := err.Error()
    return strings.Contains(msg, "broken pipe") ||
        strings.Contains(msg, "connection reset") ||
        strings.Contains(msg, "EOF")
}
