package config

import (
    "fmt"
    "os"
    "strconv"

    "github.com/joho/godotenv"
)

type Config struct {
    // Server
    Port    string
    BaseURL string

    // Auth
    WebDAVUsername string
    WebDAVPassword string

    // Telegram Bot
    BotToken  string
    ChannelID int64

    // Telegram API
    APIID   int
    APIHash string

    // Performance
    CacheSizeMB   int64
    ChunkSizeMB   int64
    MaxConns      int
    PrefetchChunks int

    // Storage
    DBPath     string
    SessionDir string
}

var Global *Config

func Load() (*Config, error) {
    // Load .env file (ignore error if not exists)
    _ = godotenv.Load()

    cfg := &Config{}

    // Server
    cfg.Port = getEnvDefault("PORT", "8080")
    cfg.BaseURL = getEnvDefault("BASE_URL", fmt.Sprintf("http://localhost:%s", cfg.Port))

    // Auth
    cfg.WebDAVUsername = mustGetEnv("WEBDAV_USERNAME")
    cfg.WebDAVPassword = mustGetEnv("WEBDAV_PASSWORD")

    // Telegram Bot
    cfg.BotToken = mustGetEnv("BOT_TOKEN")

    channelIDStr := mustGetEnv("CHANNEL_ID")
    channelID, err := strconv.ParseInt(channelIDStr, 10, 64)
    if err != nil {
        return nil, fmt.Errorf("invalid CHANNEL_ID: %w", err)
    }
    cfg.ChannelID = channelID

    // Telegram API
    apiIDStr := mustGetEnv("API_ID")
    apiID, err := strconv.Atoi(apiIDStr)
    if err != nil {
        return nil, fmt.Errorf("invalid API_ID: %w", err)
    }
    cfg.APIID = apiID
    cfg.APIHash = mustGetEnv("API_HASH")

    // Performance
    cfg.CacheSizeMB = getEnvInt64("CACHE_SIZE_MB", 512)
    cfg.ChunkSizeMB = getEnvInt64("CHUNK_SIZE_MB", 20)
    cfg.MaxConns = getEnvInt("MAX_CONNECTIONS", 50)
    cfg.PrefetchChunks = getEnvInt("PREFETCH_CHUNKS", 3)

    // Storage
    cfg.DBPath = getEnvDefault("DB_PATH", "./data/storage.db")
    cfg.SessionDir = getEnvDefault("SESSION_DIR", "./data/sessions")

    Global = cfg
    return cfg, nil
}

func mustGetEnv(key string) string {
    val := os.Getenv(key)
    if val == "" {
        panic(fmt.Sprintf("required environment variable %s is not set", key))
    }
    return val
}

func getEnvDefault(key, defaultVal string) string {
    val := os.Getenv(key)
    if val == "" {
        return defaultVal
    }
    return val
}

func getEnvInt(key string, defaultVal int) int {
    val := os.Getenv(key)
    if val == "" {
        return defaultVal
    }
    n, err := strconv.Atoi(val)
    if err != nil {
        return defaultVal
    }
    return n
}

func getEnvInt64(key string, defaultVal int64) int64 {
    val := os.Getenv(key)
    if val == "" {
        return defaultVal
    }
    n, err := strconv.ParseInt(val, 10, 64)
    if err != nil {
        return defaultVal
    }
    return n
}
