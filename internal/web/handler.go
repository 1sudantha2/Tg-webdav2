package web

import (
    "encoding/json"
    "fmt"
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
    h.mux.HandleFunc("/web/login", h.handleLogin)
    h.mux.HandleFunc("/web/logout", h.handleLogout)
    h.mux.Handle("/web", auth.WebAuth(http.HandlerFunc(h.handleApp)))
    h.mux.Handle("/web/", auth.WebAuth(http.HandlerFunc(h.handleApp)))

    apiHandler := auth.WebAuth(http.HandlerFunc(h.routeAPI))
    h.mux.Handle("/api/", apiHandler)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    h.mux.ServeHTTP(w, r)
}

func (h *Handler) routeAPI(w http.ResponseWriter, r *http.Request) {
    switch {
    case r.URL.Path == "/api/files":
        h.apiFiles(w, r)
    case r.URL.Path == "/api/folders":
        h.apiFolders(w, r)
    case r.URL.Path == "/api/move":
        h.apiMove(w, r)
    case r.URL.Path == "/api/delete":
        h.apiDelete(w, r)
    case r.URL.Path == "/api/mkdir":
        h.apiMkdir(w, r)
    case r.URL.Path == "/api/stats":
        h.apiStats(w, r)
    default:
        http.NotFound(w, r)
    }
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method == "POST" {
        if err := r.ParseForm(); err != nil {
            http.Error(w, "bad request", 400)
            return
        }
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
    w.Write([]byte(loginHTML()))
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
    if c, err := r.Cookie("session"); err == nil {
        auth.DestroySession(c.Value)
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
    w.Write([]byte(appHTML()))
}

type FileEntry struct {
    Name      string `json:"name"`
    Path      string `json:"path"`
    Size      int64  `json:"size"`
    SizeHuman string `json:"sizeHuman"`
    FileType  string `json:"fileType"`
    MimeType  string `json:"mimeType"`
    IsDir     bool   `json:"isDir"`
    ModTime   string `json:"modTime"`
}

func (h *Handler) apiFiles(w http.ResponseWriter, r *http.Request) {
    dirPath := r.URL.Query().Get("path")
    if dirPath == "" {
        dirPath = "/"
    }
    dirPath = path.Clean("/" + strings.TrimPrefix(dirPath, "/"))

    folders, _ := h.db.ListFolders(dirPath)
    files, _ := h.db.ListFiles(dirPath)

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
            SizeHuman: humanBytes(f.Size),
            FileType:  f.FileType,
            MimeType:  f.MimeType,
            IsDir:     false,
            ModTime:   f.UpdatedAt.Format(time.RFC3339),
        })
    }

    if entries == nil {
        entries = []FileEntry{}
    }
    jsonOK(w, map[string]interface{}{"path": dirPath, "entries": entries})
}

func (h *Handler) apiFolders(w http.ResponseWriter, r *http.Request) {
    var build func(string) []map[string]interface{}
    build = func(p string) []map[string]interface{} {
        fs, _ := h.db.ListFolders(p)
        var out []map[string]interface{}
        for _, f := range fs {
            out = append(out, map[string]interface{}{
                "name": f.Name, "path": f.Path, "children": build(f.Path),
            })
        }
        return out
    }
    jsonOK(w, map[string]interface{}{"tree": build("/")})
}

func (h *Handler) apiMove(w http.ResponseWriter, r *http.Request) {
    var req struct {
        OldPath string `json:"oldPath"`
        NewPath string `json:"newPath"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        jsonErr(w, "bad request", 400)
        return
    }
    var err error
    if h.db.FolderExists(req.OldPath) {
        err = h.db.MoveFolder(req.OldPath, req.NewPath, path.Base(req.NewPath))
    } else {
        err = h.db.MoveFile(req.OldPath, req.NewPath, path.Base(req.NewPath))
    }
    if err != nil {
        jsonErr(w, err.Error(), 500)
        return
    }
    jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) apiDelete(w http.ResponseWriter, r *http.Request) {
    var req struct{ Path string `json:"path"` }
    json.NewDecoder(r.Body).Decode(&req)
    var err error
    if h.db.FolderExists(req.Path) {
        err = h.db.DeleteFolder(req.Path)
    } else {
        err = h.db.DeleteFile(req.Path)
    }
    if err != nil {
        jsonErr(w, err.Error(), 500)
        return
    }
    jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) apiMkdir(w http.ResponseWriter, r *http.Request) {
    var req struct{ Path string `json:"path"` }
    json.NewDecoder(r.Body).Decode(&req)
    req.Path = path.Clean("/" + strings.TrimPrefix(req.Path, "/"))
    err := h.db.CreateFolder(&storage.FolderRecord{
        Name: path.Base(req.Path), Path: req.Path, ParentPath: path.Dir(req.Path),
    })
    if err != nil {
        jsonErr(w, err.Error(), 500)
        return
    }
    jsonOK(w, map[string]string{"status": "ok"})
}

func (h *Handler) apiStats(w http.ResponseWriter, r *http.Request) {
    files, _ := h.db.GetAllFiles()
    var total int64
    types := map[string]int{}
    for _, f := range files {
        total += f.Size
        types[f.FileType]++
    }
    jsonOK(w, map[string]interface{}{
        "totalFiles": len(files),
        "totalSize":  total,
        "sizeHuman":  humanBytes(total),
        "byType":     types,
    })
}

func jsonOK(w http.ResponseWriter, data interface{}) {
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(data)
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(code)
    json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func humanBytes(b int64) string {
    const u = 1024
    if b < u {
        return fmt.Sprintf("%d B", b)
    }
    div, exp := int64(u), 0
    for n := b / u; n >= u; n /= u {
        div *= u
        exp++
    }
    return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
