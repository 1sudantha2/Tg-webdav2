package storage

import (
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"

    "github.com/glebarez/sqlite"
    "gorm.io/gorm"
    "gorm.io/gorm/logger"
)

type FileRecord struct {
    ID        uint      `gorm:"primarykey"`
    CreatedAt time.Time
    UpdatedAt time.Time

    Name           string `gorm:"not null;index"`
    Path           string `gorm:"not null;uniqueIndex"`
    MimeType       string
    Size           int64
    FileType       string

    TelegramMsgID  int64  `gorm:"index"`
    TelegramFileID string
    ChannelID      int64

    IsDir     bool `gorm:"default:false"`
    IsDeleted bool `gorm:"default:false;index"`
}

type FolderRecord struct {
    ID         uint      `gorm:"primarykey"`
    CreatedAt  time.Time
    UpdatedAt  time.Time
    Name       string `gorm:"not null"`
    Path       string `gorm:"not null;uniqueIndex"`
    ParentPath string
}

type DB struct {
    conn *gorm.DB
}

var Instance *DB

func InitDB(dbPath string) (*DB, error) {
    if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
        return nil, err
    }

    dsn := fmt.Sprintf("%s?_journal=WAL&_timeout=5000&_fk=true", dbPath)
    conn, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
        Logger: logger.Default.LogMode(logger.Silent),
    })
    if err != nil {
        return nil, fmt.Errorf("open db: %w", err)
    }

    sqlDB, _ := conn.DB()
    sqlDB.SetMaxOpenConns(1)
    sqlDB.SetMaxIdleConns(1)

    if err := conn.AutoMigrate(&FileRecord{}, &FolderRecord{}); err != nil {
        return nil, err
    }

    db := &DB{conn: conn}
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
    for _, f := range defaults {
        db.conn.Where(FolderRecord{Path: f.Path}).FirstOrCreate(&f)
    }
}

func (db *DB) CreateFile(rec *FileRecord) error {
    return db.conn.Create(rec).Error
}

func (db *DB) GetFile(path string) (*FileRecord, error) {
    var rec FileRecord
    if err := db.conn.Where("path = ? AND is_deleted = false", path).First(&rec).Error; err != nil {
        return nil, err
    }
    return &rec, nil
}

func (db *DB) GetFileByMsgID(msgID int64) (*FileRecord, error) {
    var rec FileRecord
    if err := db.conn.Where("telegram_msg_id = ? AND is_deleted = false", msgID).First(&rec).Error; err != nil {
        return nil, err
    }
    return &rec, nil
}

func (db *DB) ListFiles(dirPath string) ([]FileRecord, error) {
    var files []FileRecord
    normalized := strings.TrimSuffix(dirPath, "/")
    err := db.conn.Where(
        "path LIKE ? AND path NOT LIKE ? AND is_deleted = false",
        normalized+"/%",
        normalized+"/%/%",
    ).Find(&files).Error
    return files, err
}

func (db *DB) ListFolders(parentPath string) ([]FolderRecord, error) {
    var folders []FolderRecord
    err := db.conn.Where("parent_path = ?", parentPath).Find(&folders).Error
    return folders, err
}

func (db *DB) GetFolder(path string) (*FolderRecord, error) {
    var rec FolderRecord
    if err := db.conn.Where("path = ?", path).First(&rec).Error; err != nil {
        return nil, err
    }
    return &rec, nil
}

func (db *DB) FolderExists(path string) bool {
    if path == "/" {
        return true
    }
    var count int64
    db.conn.Model(&FolderRecord{}).Where("path = ?", path).Count(&count)
    return count > 0
}

func (db *DB) FileExists(path string) bool {
    var count int64
    db.conn.Model(&FileRecord{}).Where("path = ? AND is_deleted = false", path).Count(&count)
    return count > 0
}

func (db *DB) CreateFolder(rec *FolderRecord) error {
    return db.conn.Create(rec).Error
}

func (db *DB) MoveFile(oldPath, newPath, newName string) error {
    return db.conn.Model(&FileRecord{}).Where("path = ?", oldPath).
        Updates(map[string]interface{}{"path": newPath, "name": newName, "updated_at": time.Now()}).Error
}

func (db *DB) DeleteFile(path string) error {
    return db.conn.Model(&FileRecord{}).Where("path = ?", path).
        Updates(map[string]interface{}{"is_deleted": true, "updated_at": time.Now()}).Error
}

func (db *DB) DeleteFolder(path string) error {
    db.conn.Where("path LIKE ?", path+"%").Delete(&FolderRecord{})
    return db.conn.Model(&FileRecord{}).Where("path LIKE ?", path+"%").
        Update("is_deleted", true).Error
}

func (db *DB) MoveFolder(oldPath, newPath, newName string) error {
    if err := db.conn.Model(&FolderRecord{}).Where("path = ?", oldPath).
        Updates(map[string]interface{}{
            "path":        newPath,
            "name":        newName,
            "parent_path": filepath.Dir(newPath),
        }).Error; err != nil {
        return err
    }

    var children []FolderRecord
    db.conn.Where("path LIKE ?", oldPath+"/%").Find(&children)
    for _, c := range children {
        np := newPath + c.Path[len(oldPath):]
        db.conn.Model(&FolderRecord{}).Where("path = ?", c.Path).
            Updates(map[string]interface{}{"path": np, "parent_path": filepath.Dir(np)})
    }

    var files []FileRecord
    db.conn.Where("path LIKE ? AND is_deleted = false", oldPath+"/%").Find(&files)
    for _, f := range files {
        np := newPath + f.Path[len(oldPath):]
        db.conn.Model(&FileRecord{}).Where("path = ?", f.Path).Update("path", np)
    }
    return nil
}

func (db *DB) GetAllFiles() ([]FileRecord, error) {
    var files []FileRecord
    err := db.conn.Where("is_deleted = false").Find(&files).Error
    return files, err
}

func (db *DB) UpdateFileSize(path string, size int64) error {
    return db.conn.Model(&FileRecord{}).Where("path = ?", path).Update("size", size).Error
}
