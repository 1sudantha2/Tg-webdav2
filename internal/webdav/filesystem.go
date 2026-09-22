package webdav

import (
    "context"
    "fmt"
    "io"
    "io/fs"
    "os"
    "path"
    "path/filepath"
    "strings"
    "time"

    "golang.org/x/net/webdav"
    "github.com/yourusername/telegram-webdav/internal/storage"
    "github.com/yourusername/telegram-webdav/internal/telegram"
    "github.com/yourusername/telegram-webdav/internal/config"
)

// TelegramFS implements webdav.FileSystem backed by Telegram
type TelegramFS struct {
    db     *storage.DB
    client *telegram.Client
    cfg    *config.Config
}

func NewTelegramFS(db *storage.DB, client *telegram.Client, cfg *config.Config) *TelegramFS {
    return &TelegramFS{db: db, client: client, cfg: cfg}
}

// ========== webdav.FileSystem interface ==========

func (fs *TelegramFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
    name = cleanPath(name)
    if fs.db.FolderExists(name) {
        return os.ErrExist
    }

    parent := path.Dir(name)
    if !fs.db.FolderExists(parent) {
        return os.ErrNotExist
    }

    return fs.db.CreateFolder(&storage.FolderRecord{
        Name:       path.Base(name),
        Path:       name,
        ParentPath: parent,
    })
}

func (tfs *TelegramFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
    name = cleanPath(name)

    // Check if it's a directory
    if tfs.db.FolderExists(name) || name == "/" {
        return &telegramDir{
            fs:   tfs,
            path: name,
        }, nil
    }

    // Check if it's a file
    rec, err := tfs.db.GetFile(name)
    if err != nil {
        // Write mode - create new file placeholder
        if flag&os.O_CREATE != 0 {
            return &telegramFile{
                fs:   tfs,
                path: name,
                rec:  nil,
            }, nil
        }
        return nil, os.ErrNotExist
    }

    return &telegramFile{
        fs:   tfs,
        path: name,
        rec:  rec,
    }, nil
}

func (tfs *TelegramFS) RemoveAll(ctx context.Context, name string) error {
    name = cleanPath(name)

    if tfs.db.FolderExists(name) {
        return tfs.db.DeleteFolder(name)
    }

    if tfs.db.FileExists(name) {
        return tfs.db.DeleteFile(name)
    }

    return os.ErrNotExist
}

func (tfs *TelegramFS) Rename(ctx context.Context, oldName, newName string) error {
    oldName = cleanPath(oldName)
    newName = cleanPath(newName)

    if tfs.db.FolderExists(oldName) {
        return tfs.db.MoveFolder(oldName, newName, path.Base(newName))
    }

    if tfs.db.FileExists(oldName) {
        return tfs.db.MoveFile(oldName, newName, path.Base(newName))
    }

    return os.ErrNotExist
}

func (tfs *TelegramFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
    name = cleanPath(name)

    if name == "/" || tfs.db.FolderExists(name) {
        folder, _ := tfs.db.GetFolder(name)
        return &fileInfo{
            name:    path.Base(name),
            size:    0,
            mode:    os.ModeDir | 0755,
            modTime: folderModTime(folder),
            isDir:   true,
        }, nil
    }

    rec, err := tfs.db.GetFile(name)
    if err != nil {
        return nil, os.ErrNotExist
    }

    return &fileInfo{
        name:    rec.Name,
        size:    rec.Size,
        mode:    0644,
        modTime: rec.UpdatedAt,
        isDir:   false,
    }, nil
}

// ========== telegramDir ==========

type telegramDir struct {
    fs      *TelegramFS
    path    string
    offset  int
    entries []os.FileInfo
}

func (d *telegramDir) Close() error { return nil }

func (d *telegramDir) Read(p []byte) (int, error) {
    return 0, io.EOF
}

func (d *telegramDir) Seek(offset int64, whence int) (int64, error) {
    return 0, nil
}

func (d *telegramDir) Readdir(count int) ([]os.FileInfo, error) {
    if d.entries == nil {
        if err := d.loadEntries(); err != nil {
            return nil, err
        }
    }

    if count <= 0 {
        all := d.entries[d.offset:]
        d.offset = len(d.entries)
        return all, nil
    }

    if d.offset >= len(d.entries) {
        return nil, io.EOF
    }

    end := d.offset + count
    if end > len(d.entries) {
        end = len(d.entries)
    }
    entries := d.entries[d.offset:end]
    d.offset = end
    return entries, nil
}

func (d *telegramDir) loadEntries() error {
    var entries []os.FileInfo

    // Load subdirectories
    folders, err := d.fs.db.ListFolders(d.path)
    if err != nil {
        return err
    }
    for _, folder := range folders {
        entries = append(entries, &fileInfo{
            name:    folder.Name,
            size:    0,
            mode:    os.ModeDir | 0755,
            modTime: folder.CreatedAt,
            isDir:   true,
        })
    }

    // Load files
    files, err := d.fs.db.ListFiles(d.path)
    if err != nil {
        return err
    }
    for _, file := range files {
        f := file
        entries = append(entries, &fileInfo{
            name:    f.Name,
            size:    f.Size,
            mode:    0644,
            modTime: f.UpdatedAt,
            isDir:   false,
        })
    }

    d.entries = entries
    return nil
}

func (d *telegramDir) Stat() (os.FileInfo, error) {
    return &fileInfo{
        name:    path.Base(d.path),
        size:    0,
        mode:    os.ModeDir | 0755,
        modTime: time.Now(),
        isDir:   true,
    }, nil
}

func (d *telegramDir) Write(p []byte) (int, error) {
    return 0, fmt.Errorf("cannot write to directory")
}

// ========== telegramFile ==========

type telegramFile struct {
    fs     *TelegramFS
    path   string
    rec    *storage.FileRecord
    reader *telegram.TelegramReader
    buf    []byte
    offset int64
}

func (f *telegramFile) Close() error {
    f.reader = nil
    return nil
}

func (f *telegramFile) Read(p []byte) (int, error) {
    if f.rec == nil {
        return 0, io.EOF
    }

    reader, err := f.getReader()
    if err != nil {
        return 0, err
    }
    return reader.Read(p)
}

func (f *telegramFile) Seek(offset int64, whence int) (int64, error) {
    if f.rec == nil {
        return 0, nil
    }

    reader, err := f.getReader()
    if err != nil {
        return 0, err
    }
    return reader.Seek(offset, whence)
}

func (f *telegramFile) Readdir(count int) ([]os.FileInfo, error) {
    return nil, fmt.Errorf("not a directory")
}

func (f *telegramFile) Stat() (os.FileInfo, error) {
    if f.rec == nil {
        return &fileInfo{
            name:    path.Base(f.path),
            size:    0,
            mode:    0644,
            modTime: time.Now(),
            isDir:   false,
        }, nil
    }

    return &fileInfo{
        name:    f.rec.Name,
        size:    f.rec.Size,
        mode:    0644,
        modTime: f.rec.UpdatedAt,
        isDir:   false,
    }, nil
}

func (f *telegramFile) Write(p []byte) (int, error) {
    // Files are uploaded via bot, not WebDAV write
    return 0, fmt.Errorf("write not supported - use Telegram bot to upload files")
}

func (f *telegramFile) getReader() (*telegram.TelegramReader, error) {
    if f.reader != nil {
        return f.reader, nil
    }

    ctx := context.Background()
    location, size, err := f.fs.client.GetFileLocation(ctx, f.rec.TelegramMsgID, f.rec.ChannelID)
    if err != nil {
        return nil, fmt.Errorf("get file location: %w", err)
    }

    // Update size in DB if needed
    if f.rec.Size == 0 && size > 0 {
        _ = f.fs.db.UpdateFileSize(f.rec.Path, size)
    }

    f.reader = telegram.NewTelegramReader(ctx, f.fs.client, location, size, f.fs.cfg)
    return f.reader, nil
}

// ========== fileInfo ==========

type fileInfo struct {
    name    string
    size    int64
    mode    os.FileMode
    modTime time.Time
    isDir   bool
}

func (fi *fileInfo) Name() string      { return fi.name }
func (fi *fileInfo) Size() int64       { return fi.size }
func (fi *fileInfo) Mode() os.FileMode { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool       { return fi.isDir }
func (fi *fileInfo) Sys() interface{}  { return nil }

// ========== Helpers ==========

func cleanPath(p string) string {
    p = path.Clean("/" + strings.TrimPrefix(p, "/"))
    return p
}

func folderModTime(folder *storage.FolderRecord) time.Time {
    if folder == nil {
        return time.Now()
    }
    return folder.UpdatedAt
}

var _ fs.FileInfo = (*fileInfo)(nil)
var _ webdav.File = (*telegramDir)(nil)
var _ webdav.File = (*telegramFile)(nil)
