package web

import (
    "encoding/json"
    "net/http"
    "path"
    "strings"
    "time"

    "github.com/yourusername/telegram-webdav/internal/auth"
    "github.com/yourusername/telegram-webdav/internal/config"
    "github.com/yourusername/telegram-webdav/internal/storage"
)

type Handler struct {
    db  *storage.DB
    cfg *config.Config
    mux *http.ServeMux
}

func NewHandler(db *storage.DB, cfg *config.Config) *Handler {
    h := &Handler{db: db, cfg: cfg, mux: http.NewServeMux()}
    h.registerRoutes()
    return h
}

func (h *Handler) registerRoutes() {
    // Public routes
    h.mux.HandleFunc("/web/login", h.handleLogin)
    h.mux.HandleFunc("/web/logout", h.handleLogout)

    // Protected routes
    protected := auth.WebAuth(http.HandlerFunc(h.handleApp))
    h.mux.Handle("/web/", protected)
    h.mux.Handle("/web", protected)

    // API routes (protected)
    apiMux := http.NewServeMux()
    apiMux.HandleFunc("/api/files", h.apiFiles)
    apiMux.HandleFunc("/api/folders", h.apiFolders)
    apiMux.HandleFunc("/api/move", h.apiMove)
    apiMux.HandleFunc("/api/delete", h.apiDelete)
    apiMux.HandleFunc("/api/mkdir", h.apiMkdir)
    apiMux.HandleFunc("/api/stats", h.apiStats)

    h.mux.Handle("/api/", auth.WebAuth(apiMux))
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    h.mux.ServeHTTP(w, r)
}

// ========== Page Handlers ==========

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method == "POST" {
        username := r.FormValue("username")
        password := r.FormValue("password")

        if username == h.cfg.WebDAVUsername && password == h.cfg.WebDAVPassword {
            token := auth.CreateSession(username)
            http.SetCookie(w, &http.Cookie{
                Name:     "session",
                Value:    token,
                Path:     "/",
                Expires:  time.Now().Add(24 * time.Hour),
                HttpOnly: true,
                SameSite: http.SameSiteLaxMode,
            })
            http.Redirect(w, r, "/web/", http.StatusFound)
            return
        }

        http.Redirect(w, r, "/web/login?error=1", http.StatusFound)
        return
    }

    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Write([]byte(loginHTML))
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
    cookie, err := r.Cookie("session")
    if err == nil {
        auth.DestroySession(cookie.Value)
    }
    http.SetCookie(w, &http.Cookie{
        Name:    "session",
        Value:   "",
        Path:    "/",
        Expires: time.Unix(0, 0),
    })
    http.Redirect(w, r, "/web/login", http.StatusFound)
}

func (h *Handler) handleApp(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Write([]byte(appHTML))
}

// ========== API Handlers ==========

type FileEntry struct {
    Name      string `json:"name"`
    Path      string `json:"path"`
    Size      int64  `json:"size"`
    SizeHuman string `json:"sizeHuman"`
    FileType  string `json:"fileType"`
    MimeType  string `json:"mimeType"`
    IsDir     bool   `json:"isDir"`
    ModTime   string `json:"modTime"`
    MsgID     int64  `json:"msgId,omitempty"`
}

func (h *Handler) apiFiles(w http.ResponseWriter, r *http.Request) {
    dirPath := r.URL.Query().Get("path")
    if dirPath == "" {
        dirPath = "/"
    }
    dirPath = path.Clean("/" + strings.TrimPrefix(dirPath, "/"))

    // Get folders
    folders, err := h.db.ListFolders(dirPath)
    if err != nil {
        jsonError(w, err.Error(), 500)
        return
    }

    // Get files
    files, err := h.db.ListFiles(dirPath)
    if err != nil {
        jsonError(w, err.Error(), 500)
        return
    }

    var entries []FileEntry

    for _, f := range folders {
        entries = append(entries, FileEntry{
            Name:    f.Name,
            Path:    f.Path,
            IsDir:   true,
            ModTime: f.UpdatedAt.Format(time.RFC3339),
        })
    }

    for _, f := range files {
        entries = append(entries, FileEntry{
            Name:      f.Name,
            Path:      f.Path,
            Size:      f.Size,
            SizeHuman: formatBytes(f.Size),
            FileType:  f.FileType,
            MimeType:  f.MimeType,
            IsDir:     false,
            ModTime:   f.UpdatedAt.Format(time.RFC3339),
            MsgID:     f.TelegramMsgID,
        })
    }

    if entries == nil {
        entries = []FileEntry{}
    }

    jsonResponse(w, map[string]interface{}{
        "path":    dirPath,
        "entries": entries,
    })
}

func (h *Handler) apiFolders(w http.ResponseWriter, r *http.Request) {
    // Return all folders tree
    var buildTree func(parentPath string) []map[string]interface{}
    buildTree = func(parentPath string) []map[string]interface{} {
        folders, _ := h.db.ListFolders(parentPath)
        var result []map[string]interface{}
        for _, f := range folders {
            children := buildTree(f.Path)
            result = append(result, map[string]interface{}{
                "name":     f.Name,
                "path":     f.Path,
                "children": children,
            })
        }
        return result
    }

    tree := buildTree("/")
    jsonResponse(w, map[string]interface{}{"tree": tree})
}

func (h *Handler) apiMove(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "method not allowed", 405)
        return
    }

    var req struct {
        OldPath string `json:"oldPath"`
        NewPath string `json:"newPath"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        jsonError(w, "invalid request", 400)
        return
    }

    var err error
    if h.db.FolderExists(req.OldPath) {
        err = h.db.MoveFolder(req.OldPath, req.NewPath, path.Base(req.NewPath))
    } else {
        err = h.db.MoveFile(req.OldPath, req.NewPath, path.Base(req.NewPath))
    }

    if err != nil {
        jsonError(w, err.Error(), 500)
        return
    }

    jsonResponse(w, map[string]string{"status": "ok"})
}

func (h *Handler) apiDelete(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "method not allowed", 405)
        return
    }

    var req struct {
        Path string `json:"path"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        jsonError(w, "invalid request", 400)
        return
    }

    var err error
    if h.db.FolderExists(req.Path) {
        err = h.db.DeleteFolder(req.Path)
    } else {
        err = h.db.DeleteFile(req.Path)
    }

    if err != nil {
        jsonError(w, err.Error(), 500)
        return
    }

    jsonResponse(w, map[string]string{"status": "ok"})
}

func (h *Handler) apiMkdir(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "method not allowed", 405)
        return
    }

    var req struct {
        Path string `json:"path"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        jsonError(w, "invalid request", 400)
        return
    }

    req.Path = path.Clean("/" + strings.TrimPrefix(req.Path, "/"))

    if h.db.FolderExists(req.Path) {
        jsonError(w, "folder already exists", 409)
        return
    }

    err := h.db.CreateFolder(&storage.FolderRecord{
        Name:       path.Base(req.Path),
        Path:       req.Path,
        ParentPath: path.Dir(req.Path),
    })

    if err != nil {
        jsonError(w, err.Error(), 500)
        return
    }

    jsonResponse(w, map[string]string{"status": "ok"})
}

func (h *Handler) apiStats(w http.ResponseWriter, r *http.Request) {
    files, _ := h.db.GetAllFiles()
    var totalSize int64
    typeCount := make(map[string]int)

    for _, f := range files {
        totalSize += f.Size
        typeCount[f.FileType]++
    }

    jsonResponse(w, map[string]interface{}{
        "totalFiles":  len(files),
        "totalSize":   totalSize,
        "sizeHuman":   formatBytes(totalSize),
        "byType":      typeCount,
    })
}

// ========== Helpers ==========

func jsonResponse(w http.ResponseWriter, data interface{}) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func formatBytes(bytes int64) string {
    const unit = 1024
    if bytes < unit {
        return fmt.Sprintf("%d B", bytes)
    }
    div, exp := int64(unit), 0
    for n := bytes / unit; n >= unit; n /= unit {
        div *= unit
        exp++
    }
    return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

import "fmt"
