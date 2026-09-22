package auth

import (
    "crypto/subtle"
    "encoding/base64"
    "fmt"
    "net/http"
    "sync"
    "time"

    "github.com/yourusername/telegram-webdav/internal/config"
)

// BasicAuth handles WebDAV HTTP Basic Authentication
func BasicAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        username, password, ok := r.BasicAuth()
        if !ok {
            unauthorized(w)
            return
        }

        cfg := config.Global
        validUser := subtle.ConstantTimeCompare([]byte(username), []byte(cfg.WebDAVUsername)) == 1
        validPass := subtle.ConstantTimeCompare([]byte(password), []byte(cfg.WebDAVPassword)) == 1

        if !validUser || !validPass {
            unauthorized(w)
            return
        }

        next.ServeHTTP(w, r)
    })
}

func unauthorized(w http.ResponseWriter) {
    w.Header().Set("WWW-Authenticate", `Basic realm="Telegram WebDAV"`)
    http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

// ======= Session Management =======

type sessionEntry struct {
    username  string
    expiresAt time.Time
}

var (
    sessionsMu sync.RWMutex
    sessionMap = make(map[string]sessionEntry)
)

func CreateSession(username string) string {
    token := base64.URLEncoding.EncodeToString([]byte(
        fmt.Sprintf("%s-%d", username, time.Now().UnixNano()),
    ))
    sessionsMu.Lock()
    sessionMap[token] = sessionEntry{
        username:  username,
        expiresAt: time.Now().Add(24 * time.Hour),
    }
    sessionsMu.Unlock()
    return token
}

func ValidateSession(token string) (string, bool) {
    sessionsMu.RLock()
    entry, ok := sessionMap[token]
    sessionsMu.RUnlock()

    if !ok || time.Now().After(entry.expiresAt) {
        if ok {
            sessionsMu.Lock()
            delete(sessionMap, token)
            sessionsMu.Unlock()
        }
        return "", false
    }
    return entry.username, true
}

func DestroySession(token string) {
    sessionsMu.Lock()
    delete(sessionMap, token)
    sessionsMu.Unlock()
}

// WebAuth middleware for browser-based UI
func WebAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        cookie, err := r.Cookie("session")
        if err != nil {
            http.Redirect(w, r, "/web/login", http.StatusFound)
            return
        }

        if _, ok := ValidateSession(cookie.Value); !ok {
            http.Redirect(w, r, "/web/login", http.StatusFound)
            return
        }

        next.ServeHTTP(w, r)
    })
}

func init() {
    go func() {
        for range time.Tick(time.Hour) {
            now := time.Now()
            sessionsMu.Lock()
            for token, entry := range sessionMap {
                if now.After(entry.expiresAt) {
                    delete(sessionMap, token)
                }
            }
            sessionsMu.Unlock()
        }
    }()
}
