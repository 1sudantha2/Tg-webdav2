package telegram

import (
    "context"
    "fmt"
    "io"
    "os"
    "sync"
    "time"

    "github.com/gotd/td/session"
    "github.com/gotd/td/telegram"
    "github.com/gotd/td/telegram/auth"
    "github.com/gotd/td/telegram/downloader"
    "github.com/gotd/td/tg"
    "github.com/yourusername/telegram-webdav/internal/config"
    "github.com/yourusername/telegram-webdav/internal/storage"
    "go.uber.org/zap"
)

type Client struct {
    tgClient   *telegram.Client
    api        *tg.Client
    downloader *downloader.Downloader
    cache      *storage.ChunkCache
    cfg        *config.Config
    ready      chan struct{}
    mu         sync.Mutex
    logger     *zap.Logger
}

var TGClient *Client

func NewClient(cfg *config.Config) (*Client, error) {
    if err := os.MkdirAll(cfg.SessionDir, 0755); err != nil {
        return nil, err
    }

    logger, _ := zap.NewProduction()

    sessionStorage := &session.FileStorage{
        Path: fmt.Sprintf("%s/session.json", cfg.SessionDir),
    }

    tgClient := telegram.NewClient(cfg.APIID, cfg.APIHash, telegram.Options{
        SessionStorage: sessionStorage,
        Logger:         logger,
        UpdateHandler:  telegram.UpdateHandlerFunc(nil),
    })

    c := &Client{
        tgClient: tgClient,
        cache:    storage.NewChunkCache(cfg.CacheSizeMB),
        cfg:      cfg,
        ready:    make(chan struct{}),
        logger:   logger,
    }

    TGClient = c
    return c, nil
}

func (c *Client) Start(ctx context.Context) error {
    return c.tgClient.Run(ctx, func(ctx context.Context) error {
        // Authenticate as bot
        flow := auth.NewFlow(
            auth.Bot(c.cfg.BotToken),
            auth.SendCodeOptions{},
        )
        if err := c.tgClient.Auth().IfNecessary(ctx, flow); err != nil {
            return fmt.Errorf("auth failed: %w", err)
        }

        c.api = c.tgClient.API()
        c.downloader = downloader.NewDownloader()

        close(c.ready)
        c.logger.Info("Telegram client ready")

        <-ctx.Done()
        return ctx.Err()
    })
}

func (c *Client) WaitReady(ctx context.Context) error {
    select {
    case <-c.ready:
        return nil
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(30 * time.Second):
        return fmt.Errorf("telegram client not ready after 30s")
    }
}

// GetFileLocation retrieves the downloadable location for a message
func (c *Client) GetFileLocation(ctx context.Context, msgID int64, channelID int64) (tg.InputFileLocationClass, int64, error) {
    inputChannel := &tg.InputChannel{
        ChannelID:  channelID,
        AccessHash: 0,
    }

    // Resolve channel
    ch, err := c.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{inputChannel})
    if err != nil {
        return nil, 0, fmt.Errorf("get channel: %w", err)
    }

    if len(ch.GetChats()) == 0 {
        return nil, 0, fmt.Errorf("channel not found")
    }

    // Get messages
    msgs, err := c.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
        Channel: inputChannel,
        ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: int(msgID)}},
    })
    if err != nil {
        return nil, 0, fmt.Errorf("get messages: %w", err)
    }

    var fileSize int64
    var location tg.InputFileLocationClass

    for _, msg := range msgs.(*tg.MessagesChannelMessages).Messages {
        m, ok := msg.(*tg.Message)
        if !ok {
            continue
        }
        if m.Media == nil {
            continue
        }

        switch media := m.Media.(type) {
        case *tg.MessageMediaDocument:
            doc, ok := media.Document.(*tg.Document)
            if !ok {
                continue
            }
            fileSize = doc.Size
            location = &tg.InputDocumentFileLocation{
                ID:            doc.ID,
                AccessHash:    doc.AccessHash,
                FileReference: doc.FileReference,
            }
        case *tg.MessageMediaPhoto:
            photo, ok := media.Photo.(*tg.Photo)
            if !ok {
                continue
            }
            if len(photo.Sizes) == 0 {
                continue
            }
            // Get largest size
            largest := photo.Sizes[len(photo.Sizes)-1]
            if ps, ok := largest.(*tg.PhotoSize); ok {
                fileSize = int64(ps.Size)
            }
            location = &tg.InputPhotoFileLocation{
                ID:            photo.ID,
                AccessHash:    photo.AccessHash,
                FileReference: photo.FileReference,
                ThumbSize:     "y",
            }
        }
    }

    if location == nil {
        return nil, 0, fmt.Errorf("no downloadable media found in message")
    }

    return location, fileSize, nil
}

// DownloadChunk downloads a specific byte range of a file
func (c *Client) DownloadChunk(ctx context.Context, location tg.InputFileLocationClass, offset, limit int64) ([]byte, error) {
    // Check cache first
    cacheKey := storage.ChunkKey{
        FileID: fmt.Sprintf("%v", location),
        Chunk:  offset / limit,
    }

    if data, ok := c.cache.Get(cacheKey); ok {
        return data, nil
    }

    // Download from Telegram
    var buf []byte
    _, err := c.downloader.Download(c.api, location).
        Offset(offset).
        Limit(int(limit)).
        Stream(ctx, func(ctx context.Context, r io.Reader) error {
            data, err := io.ReadAll(r)
            if err != nil {
                return err
            }
            buf = data
            return nil
        })
    if err != nil {
        return nil, fmt.Errorf("download chunk: %w", err)
    }

    // Cache the chunk
    c.cache.Put(cacheKey, buf)
    return buf, nil
}

// ForwardToChannel forwards a message to the storage channel
func (c *Client) ForwardToChannel(ctx context.Context, fromChatID, msgID int64) (*tg.Updates, error) {
    // Source: user's chat (bot inbox = bot's own chat)
    from := &tg.InputPeerUser{
        UserID: fromChatID,
    }

    // Destination: channel
    to := &tg.InputPeerChannel{
        ChannelID: c.cfg.ChannelID,
    }

    return c.api.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
        FromPeer: from,
        ID:       []int{int(msgID)},
        ToPeer:   to,
        RandomID: []int64{time.Now().UnixNano()},
    })
}
