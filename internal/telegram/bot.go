package telegram

import (
    "fmt"
    "log"
    "math/rand"
    "mime"
    "path/filepath"
    "strings"
    "time"

    tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
    "github.com/yourusername/telegram-webdav/internal/config"
    "github.com/yourusername/telegram-webdav/internal/storage"
)

type Bot struct {
    api    *tgbotapi.BotAPI
    cfg    *config.Config
    db     *storage.DB
}

func NewBot(cfg *config.Config, db *storage.DB) (*Bot, error) {
    api, err := tgbotapi.NewBotAPI(cfg.BotToken)
    if err != nil {
        return nil, fmt.Errorf("create bot: %w", err)
    }
    api.Debug = false
    log.Printf("Bot authorized as: %s", api.Self.UserName)

    return &Bot{api: api, cfg: cfg, db: db}, nil
}

func (b *Bot) Start() {
    u := tgbotapi.NewUpdate(0)
    u.Timeout = 60
    updates := b.api.GetUpdatesChan(u)

    for update := range updates {
        if update.Message == nil {
            continue
        }
        go b.handleMessage(update.Message)
    }
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
    // Only process messages with files
    if !b.hasMedia(msg) {
        if msg.Text != "" {
            b.sendMessage(msg.Chat.ID, "📁 Send me a file to store it in WebDAV!\n\nAdd a caption to set the filename.")
        }
        return
    }

    // Forward to channel first
    forwardMsg := tgbotapi.NewForward(b.cfg.ChannelID, msg.Chat.ID, msg.MessageID)
    sent, err := b.api.Send(forwardMsg)
    if err != nil {
        log.Printf("Forward error: %v", err)
        b.sendMessage(msg.Chat.ID, "❌ Failed to store file. Please try again.")
        return
    }

    // Detect file info
    fileInfo := b.extractFileInfo(msg)
    
    // Determine filename from caption or generate
    filename := b.determineFilename(msg.Caption, fileInfo)
    
    // Default folder is /general
    folderPath := "/general"
    filePath := folderPath + "/" + filename

    // Ensure unique path
    filePath = b.ensureUniquePath(filePath)
    filename = filepath.Base(filePath)

    // Save to DB
    rec := &storage.FileRecord{
        Name:          filename,
        Path:          filePath,
        MimeType:      fileInfo.MimeType,
        Size:          fileInfo.Size,
        FileType:      fileInfo.FileType,
        TelegramMsgID: int64(sent.MessageID),
        TelegramFileID: fileInfo.FileID,
        ChannelID:     b.cfg.ChannelID,
    }

    if err := b.db.CreateFile(rec); err != nil {
        log.Printf("DB error: %v", err)
        b.sendMessage(msg.Chat.ID, "❌ Failed to index file.")
        return
    }

    emoji := fileTypeEmoji(fileInfo.FileType)
    b.sendMessage(msg.Chat.ID, fmt.Sprintf(
        "%s File stored successfully!\n\n"+
        "📂 Path: `%s`\n"+
        "📝 Name: `%s`\n"+
        "📊 Size: %s\n"+
        "🗂 Type: %s",
        emoji, filePath, filename,
        formatBytes(fileInfo.Size), fileInfo.FileType,
    ))

    log.Printf("Stored file: %s -> msg:%d", filePath, sent.MessageID)
}

type FileInfo struct {
    FileID   string
    MimeType string
    Size     int64
    FileType string
    Ext      string
}

func (b *Bot) hasMedia(msg *tgbotapi.Message) bool {
    return msg.Document != nil || msg.Photo != nil ||
        msg.Video != nil || msg.Audio != nil ||
        msg.Voice != nil || msg.VideoNote != nil ||
        msg.Animation != nil || msg.Sticker != nil
}

func (b *Bot) extractFileInfo(msg *tgbotapi.Message) FileInfo {
    info := FileInfo{}

    switch {
    case msg.Document != nil:
        doc := msg.Document
        info.FileID = doc.FileID
        info.MimeType = doc.MimeType
        info.Size = int64(doc.FileSize)
        info.FileType = "document"
        info.Ext = filepath.Ext(doc.FileName)
        if info.Ext == "" {
            info.Ext = mimeToExt(doc.MimeType)
        }
        // Refine type from MIME
        info.FileType = detectFileType(doc.MimeType)

    case msg.Photo != nil:
        photos := msg.Photo
        largest := photos[len(photos)-1]
        info.FileID = largest.FileID
        info.MimeType = "image/jpeg"
        info.Size = int64(largest.FileSize)
        info.FileType = "image"
        info.Ext = ".jpg"

    case msg.Video != nil:
        vid := msg.Video
        info.FileID = vid.FileID
        info.MimeType = vid.MimeType
        info.Size = int64(vid.FileSize)
        info.FileType = "video"
        info.Ext = mimeToExt(vid.MimeType)
        if info.Ext == "" {
            info.Ext = ".mp4"
        }

    case msg.Audio != nil:
        audio := msg.Audio
        info.FileID = audio.FileID
        info.MimeType = audio.MimeType
        info.Size = int64(audio.FileSize)
        info.FileType = "audio"
        info.Ext = mimeToExt(audio.MimeType)
        if info.Ext == "" {
            info.Ext = ".mp3"
        }

    case msg.Voice != nil:
        info.FileID = msg.Voice.FileID
        info.MimeType = "audio/ogg"
        info.Size = int64(msg.Voice.FileSize)
        info.FileType = "audio"
        info.Ext = ".ogg"

    case msg.Animation != nil:
        info.FileID = msg.Animation.FileID
        info.MimeType = msg.Animation.MimeType
        info.Size = int64(msg.Animation.FileSize)
        info.FileType = "video"
        info.Ext = ".gif"
    }

    return info
}

func (b *Bot) determineFilename(caption string, info FileInfo) string {
    var base string

    if caption != "" {
        // Clean caption for use as filename
        base = cleanFilename(caption)
        // Add extension if not present
        if filepath.Ext(base) == "" && info.Ext != "" {
            base += info.Ext
        }
    } else {
        // Generate random name
        base = randomName() + info.Ext
    }

    return base
}

func (b *Bot) ensureUniquePath(path string) string {
    if !b.db.FileExists(path) {
        return path
    }

    ext := filepath.Ext(path)
    base := strings.TrimSuffix(path, ext)

    for i := 1; i < 1000; i++ {
        newPath := fmt.Sprintf("%s_%d%s", base, i, ext)
        if !b.db.FileExists(newPath) {
            return newPath
        }
    }
    return fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext)
}

func (b *Bot) sendMessage(chatID int64, text string) {
    msg := tgbotapi.NewMessage(chatID, text)
    msg.ParseMode = "Markdown"
    _, _ = b.api.Send(msg)
}

// =========== Helpers ===========

func cleanFilename(name string) string {
    // Replace invalid chars
    replacer := strings.NewReplacer(
        "/", "_", "\\", "_", ":", "_",
        "*", "_", "?", "_", "\"", "_",
        "<", "_", ">", "_", "|", "_",
    )
    name = replacer.Replace(name)
    name = strings.TrimSpace(name)
    if len(name) > 200 {
        name = name[:200]
    }
    return name
}

func randomName() string {
    const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
    b := make([]byte, 8)
    for i := range b {
        b[i] = charset[rand.Intn(len(charset))]
    }
    return "file_" + string(b)
}

func detectFileType(mimeType string) string {
    mimeType = strings.ToLower(mimeType)
    switch {
    case strings.HasPrefix(mimeType, "image/"):
        return "image"
    case strings.HasPrefix(mimeType, "video/"):
        return "video"
    case strings.HasPrefix(mimeType, "audio/"):
        return "audio"
    default:
        return "document"
    }
}

func mimeToExt(mimeType string) string {
    exts, err := mime.ExtensionsByType(mimeType)
    if err != nil || len(exts) == 0 {
        return ""
    }
    return exts[0]
}

func fileTypeEmoji(fileType string) string {
    switch fileType {
    case "image":
        return "🖼"
    case "video":
        return "🎬"
    case "audio":
        return "🎵"
    default:
        return "📄"
    }
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
