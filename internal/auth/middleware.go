package auth

import (
    "crypto/subtle"
    "encoding/base64"
    "net/http"
    "strings"
    "time"

    "github.com/yourusername/telegram-webdav/internal/config"
)

// BasicAuth middleware for WebDAV
func BasicAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        username, password, ok := r.BasicAuth()
        if !ok {
            w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV Storage"`)
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }

        cfg := config.Global
        validUser := subtle.ConstantTimeCompare([]byte(username), []byte(cfg.WebDAVUsername)) == 1
        validPass := subtle.ConstantTimeCompare([]byte(password), []byte(cfg.WebDAVPassword)) == 1

        if !validUser || !validPass {
            w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV Storage"`)
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }

        next.ServeHTTP(w, r)
    })
}

// SessionAuth for web UI
type SessionStore struct {
    sessions map[string]sessionEntry
}

type sessionEntry struct {
    username  string
    expiresAt time.Time
}

var sessions = &SessionStore{
    sessions: make(map[string]sessionEntry),
}

func CreateSession(username string) string {
    token := base64.URLEncoding.EncodeToString([]byte(
        fmt.Sprintf("%s-%d", username, time.Now().UnixNano()),
    ))
    sessions.sessions[token] = sessionEntry{
        username:  username,
        expiresAt: time.Now().Add(24 * time.Hour),
    }
    return token
}

func ValidateSession(token string) (string, bool) {
    entry, ok := sessions.sessions[token]
    if !ok {
        return "", false
    }
    if time.Now().After(entry.expiresAt) {
        delete(sessions.sessions, token)
        return "", false
    }
    return entry.username, true
}

func WebAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Check session cookie
        cookie, err := r.Cookie("session")
        if err != nil {
            http.Redirect(w, r, "/web/login", http.StatusFound)
            return
        }

        _, ok := ValidateSession(cookie.Value)
        if !ok {
            http.Redirect(w, r, "/web/login", http.StatusFound)
            return
        }

        next.ServeHTTP(w, r)
    })
}

// Fix missing import
import "fmt"

func init() {
    // Cleanup expired sessions periodically
    go func() {
        ticker := time.NewTicker(1 * time.Hour)
        for range ticker.C {
            now := time.Now()
            for token, entry := range sessions.sessions {
                if now.After(entry.expiresAt) {
                    delete(sessions.sessions, token)
                }
            }
        }
    }()
}

// CorrectAuth - Fixed version without init import trick
