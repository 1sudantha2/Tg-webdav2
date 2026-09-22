package storage

import (
    "fmt"
    "path/filepath"
    "strings"
    "time"

    "github.com/glebarez/sqlite"
    "gorm.io/gorm"
    "gorm.io/gorm/logger"
)

// FileRecord represents a file stored in Telegram
type FileRecord struct {
    ID           uint      `gorm:"primarykey"`
    CreatedAt    time.Time
    UpdatedAt    time.Time
    
    Name         string    `gorm:"not null;index"`
    Path         string    `gorm:"not null;uniqueIndex"` // /general/video.mp4
    MimeType     string
    Size         int64
    FileType     string    // image, video, audio, document
    
    // Telegram references
    TelegramMsgID int64   `gorm:"index"`
    TelegramFileID string
    ChannelID     int64
    
    // Metadata
    IsDir        bool      `gorm:"default:false"`
    IsDeleted    bool      `gorm:"default:false;index"`
}

// FolderRecord represents a virtual folder
type FolderRecord struct {
    ID        uint      `gorm:"primarykey"`
    CreatedAt time.Time
    UpdatedAt time.Time
    
    Name      string    `gorm:"not null"`
    Path      string    `gorm:"not null;uniqueIndex"`
    ParentPath string
}

type DB struct {
    conn *gorm.DB
}

var Instance *DB

func InitDB(dbPath string) (*DB, error) {
    // Ensure directory exists
    dir := filepath.Dir(dbPath)
    if err := ensureDir(dir); err != nil {
        return nil, err
    }

    conn, err := gorm.Open(sqlite.Open(dbPath+"?_journal=WAL&_timeout=5000&_fk=true"), &gorm.Config{
        Logger: logger.Default.LogMode(logger.Silent),
    })
    if err != nil {
        return nil, fmt.Errorf("failed to open database: %w", err)
    }

    // Connection pool settings
    sqlDB, err := conn.DB()
    if err != nil {
        return nil, err
    }
    sqlDB.SetMaxOpenConns(1) // SQLite single writer
    sqlDB.SetMaxIdleConns(1)

    // Auto migrate
    if err := conn.AutoMigrate(&FileRecord{}, &FolderRecord{}); err != nil {
        return nil, fmt.Errorf("migration failed: %w", err)
    }

    db := &DB{conn: conn}
    
    // Create default folders
    db.ensureDefaultFolders()
    
    Instance = db
    return db, nil
}

func (db *DB) ensureDefaultFolders() {
    defaults := []FolderRecord{
        {Name: "general", Path: "/general", ParentPath: "/"},
        {Name: "images", Path: "/images", ParentPath: "/"},
        {Name: "videos", Path: "/videos", ParentPath: "/"},
        {Name: "audio", Path: "/audio", ParentPath: "/"},
        {Name: "documents", Path: "/documents", ParentPath: "/"},
    }
    for _, folder := range defaults {
        db.conn.FirstOrCreate(&folder, FolderRecord{Path: folder.Path})
    }
}

// =========== File Operations ===========

func (db *DB) CreateFile(rec *FileRecord) error {
    return db.conn.Create(rec).Error
}

func (db *DB) GetFile(path string) (*FileRecord, error) {
    var rec FileRecord
    result := db.conn.Where("path = ? AND is_deleted = false", path).First(&rec)
    if result.Error != nil {
        return nil, result.Error
    }
    return &rec, nil
}

func (db *DB) GetFileByMsgID(msgID int64) (*FileRecord, error) {
    var rec FileRecord
    result := db.conn.Where("telegram_msg_id = ? AND is_deleted = false", msgID).First(&rec)
    if result.Error != nil {
        return nil, result.Error
    }
    return &rec, nil
}

func (db *DB) ListFiles(dirPath string) ([]FileRecord, error) {
    var files []FileRecord
    
    // Match files directly in this directory (not subdirectories)
    normalizedPath := strings.TrimSuffix(dirPath, "/")
    
    result := db.conn.Where(
        "path LIKE ? AND path NOT LIKE ? AND is_deleted = false",
        normalizedPath+"/%",
        normalizedPath+"/%/%",
    ).Find(&files)
    
    return files, result.Error
}

func (db *DB) MoveFile(oldPath, newPath, newName string) error {
    return db.conn.Model(&FileRecord{}).
        Where("path = ?", oldPath).
        Updates(map[string]interface{}{
            "path":       newPath,
            "name":       newName,
            "updated_at": time.Now(),
        }).Error
}

func (db *DB) DeleteFile(path string) error {
    return db.conn.Model(&FileRecord{}).
        Where("path = ?", path).
        Updates(map[string]interface{}{
            "is_deleted": true,
            "updated_at": time.Now(),
        }).Error
}

func (db *DB) UpdateFileSize(path string, size int64) error {
    return db.conn.Model(&FileRecord{}).
        Where("path = ?", path).
        Update("size", size).Error
}

func (db *DB) FileExists(path string) bool {
    var count int64
    db.conn.Model(&FileRecord{}).
        Where("path = ? AND is_deleted = false", path).
        Count(&count)
    return count > 0
}

func (db *DB) GetAllFiles() ([]FileRecord, error) {
    var files []FileRecord
    result := db.conn.Where("is_deleted = false").Find(&files)
    return files, result.Error
}

// =========== Folder Operations ===========

func (db *DB) CreateFolder(rec *FolderRecord) error {
    return db.conn.Create(rec).Error
}

func (db *DB) GetFolder(path string) (*FolderRecord, error) {
    var rec FolderRecord
    result := db.conn.Where("path = ?", path).First(&rec)
    if result.Error != nil {
        return nil, result.Error
    }
    return &rec, nil
}

func (db *DB) ListFolders(parentPath string) ([]FolderRecord, error) {
    var folders []FolderRecord
    result := db.conn.Where("parent_path = ?", parentPath).Find(&folders)
    return folders, result.Error
}

func (db *DB) FolderExists(path string) bool {
    if path == "/" {
        return true
    }
    var count int64
    db.conn.Model(&FolderRecord{}).Where("path = ?", path).Count(&count)
    return count > 0
}

func (db *DB) DeleteFolder(path string) error {
    // Delete folder and all its contents
    db.conn.Where("path LIKE ?", path+"%").Delete(&FolderRecord{})
    db.conn.Model(&FileRecord{}).
        Where("path LIKE ?", path+"%").
        Update("is_deleted", true)
    return nil
}

func (db *DB) MoveFolder(oldPath, newPath, newName string) error {
    // Update folder record
    if err := db.conn.Model(&FolderRecord{}).
        Where("path = ?", oldPath).
        Updates(map[string]interface{}{
            "path":        newPath,
            "name":        newName,
            "parent_path": filepath.Dir(newPath),
        }).Error; err != nil {
        return err
    }

    // Update all child folders
    var children []FolderRecord
    db.conn.Where("path LIKE ?", oldPath+"/%").Find(&children)
    for _, child := range children {
        newChildPath := newPath + child.Path[len(oldPath):]
        db.conn.Model(&FolderRecord{}).
            Where("path = ?", child.Path).
            Updates(map[string]interface{}{
                "path":        newChildPath,
                "parent_path": filepath.Dir(newChildPath),
            })
    }

    // Update all files
    var files []FileRecord
    db.conn.Where("path LIKE ? AND is_deleted = false", oldPath+"/%").Find(&files)
    for _, file := range files {
        newFilePath := newPath + file.Path[len(oldPath):]
        db.conn.Model(&FileRecord{}).
            Where("path = ?", file.Path).
            Update("path", newFilePath)
    }

    return nil
}

func ensureDir(dir string) error {
    import_os := func() error {
        import "os"
        return os.MkdirAll(dir, 0755)
    }
    _ = import_os
    return nil
}
