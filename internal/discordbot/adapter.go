package discordbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Phrixos-git/zii/internal/orchestrator"
	"github.com/Phrixos-git/zii/internal/storage"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

const (
	defaultReplyMaxChars = 1900
	maxInputChars        = 4000
	userRate             = rate.Limit(5.0 / 60.0)
)

type Incoming struct {
	ID, GuildID, ChannelID, ThreadID, UserID, Content string
	CreatedAt, ReceivedAt                             time.Time
	IsDM, AuthorBot, Webhook, System                  bool
}

type SentMessage struct {
	ID        string
	CreatedAt time.Time
}

type ReplySender interface {
	Reply(context.Context, string, string, string) (SentMessage, error)
}
type Processor interface {
	ProcessQueuedWith(context.Context, *orchestrator.RequestQueue, orchestrator.Request, func(context.Context, orchestrator.Reply) error) error
	RecordSuccessfulReply(context.Context, orchestrator.Reply, orchestrator.DiscordReplyResult) error
}

var ErrReplyDelivery = errors.New("discordbot: reply delivery failed")

type Adapter struct {
	processor  Processor
	queue      *orchestrator.RequestQueue
	sender     ReplySender
	maxChars   int
	maxRetries int
	mu         sync.Mutex
	inFlight   map[string]struct{}
	limiters   map[string]userLimiter
	closed     bool
	active     sync.WaitGroup
}

type userLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type Config struct{ ReplyMaxChars, SendMaxRetries int }

func New(processor Processor, queue *orchestrator.RequestQueue, sender ReplySender, cfg Config) (*Adapter, error) {
	if processor == nil || queue == nil || sender == nil {
		return nil, errors.New("discordbot: processor, queue, and sender are required")
	}
	if cfg.ReplyMaxChars == 0 {
		cfg.ReplyMaxChars = defaultReplyMaxChars
	}
	if cfg.ReplyMaxChars < 1 || cfg.ReplyMaxChars > 2000 {
		return nil, errors.New("discordbot: reply max chars must be between 1 and 2000")
	}
	if cfg.SendMaxRetries == 0 {
		cfg.SendMaxRetries = 3
	}
	if cfg.SendMaxRetries < 0 || cfg.SendMaxRetries > 10 {
		return nil, errors.New("discordbot: send max retries must be between 0 and 10")
	}
	return &Adapter{processor: processor, queue: queue, sender: sender, maxChars: cfg.ReplyMaxChars, maxRetries: cfg.SendMaxRetries, inFlight: make(map[string]struct{}), limiters: make(map[string]userLimiter)}, nil
}

func (a *Adapter) Handle(ctx context.Context, msg Incoming, botID string) {
	if a == nil || ctx == nil || msg.ID == "" || msg.AuthorBot || msg.Webhook || msg.System {
		return
	}
	if msg.IsDM { /* direct messages are accepted without mention */
	} else {
		if botID == "" || !hasMention(msg.Content, botID) {
			return
		}
		msg.Content = removeMention(msg.Content, botID)
	}
	content, ok := normalizeInput(msg.Content)
	if !ok {
		return
	}
	msg.Content = content
	if strings.TrimSpace(msg.UserID) == "" || strings.TrimSpace(msg.ChannelID) == "" || msg.CreatedAt.IsZero() {
		return
	}
	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = time.Now().UTC()
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	if _, exists := a.inFlight[msg.ID]; exists {
		a.mu.Unlock()
		return
	}
	entry, ok := a.limiters[msg.UserID]
	if !ok {
		entry = userLimiter{limiter: rate.NewLimiter(userRate, 2)}
	}
	entry.lastSeen = time.Now()
	a.limiters[msg.UserID] = entry
	if len(a.limiters) > 4096 {
		cutoff := time.Now().Add(-time.Minute)
		for id, old := range a.limiters {
			if old.lastSeen.Before(cutoff) {
				delete(a.limiters, id)
			}
		}
	}
	if !entry.limiter.Allow() {
		a.mu.Unlock()
		return
	}
	a.inFlight[msg.ID] = struct{}{}
	a.active.Add(1)
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.inFlight, msg.ID); a.mu.Unlock(); a.active.Done() }()
	request := orchestrator.Request{RequestID: uuid.NewString(), DiscordMessageID: msg.ID, GuildID: nullable(msg.GuildID), ChannelID: msg.ChannelID, ThreadID: msg.ThreadID, UserID: msg.UserID, Content: msg.Content, MessageCreatedAt: msg.CreatedAt, ReceivedAt: msg.ReceivedAt}
	replyChannelID := msg.ChannelID
	if msg.ThreadID != "" {
		replyChannelID = msg.ThreadID
	}
	started := time.Now()
	err := a.processor.ProcessQueuedWith(ctx, a.queue, request, func(workCtx context.Context, result orchestrator.Reply) error {
		chunks := splitMessage(result.Content, a.maxChars)
		if len(chunks) == 0 {
			return ErrReplyDelivery
		}
		var first SentMessage
		for i, chunk := range chunks {
			sent, sendErr := a.sendWithRetry(workCtx, msg.ID, replyChannelID, chunk)
			if sendErr != nil {
				code := "discord_send_failed"
				if i > 0 {
					code = "partial_reply"
				}
				slog.Error("Discord reply failed", "component", "discord_bot", "event", "reply_failed", "request_id", request.RequestID, "discord_message_id", msg.ID, "channel_id", msg.ChannelID, "duration_ms", time.Since(started).Milliseconds(), "status", "failed", "error_code", code)
				return fmt.Errorf("%w: send failed", ErrReplyDelivery)
			}
			if i == 0 {
				first = sent
			}
		}
		if persistErr := a.processor.RecordSuccessfulReply(workCtx, result, orchestrator.DiscordReplyResult{RequestID: result.RequestID, DiscordMessageID: first.ID, CreatedAt: first.CreatedAt}); persistErr != nil {
			slog.Error("Discord reply persistence failed", "component", "discord_bot", "event", "assistant_persist_failed", "request_id", request.RequestID, "discord_message_id", msg.ID, "duration_ms", time.Since(started).Milliseconds(), "status", "failed", "error_code", "storage_error")
			return fmt.Errorf("%w: persistence failed", ErrReplyDelivery)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrReplyDelivery) {
			return
		}
		if errors.Is(err, storage.ErrDuplicateDiscordMessage) {
			return
		}
		code := "request_failed"
		if errors.Is(err, orchestrator.ErrBusy) {
			code = "busy"
		} else if errors.Is(err, orchestrator.ErrQueueWaitTimeout) {
			code = "queue_timeout"
		}
		slog.Warn("Discord request failed", "component", "discord_bot", "event", "request_failed", "request_id", request.RequestID, "discord_message_id", msg.ID, "guild_id", msg.GuildID, "channel_id", msg.ChannelID, "user_id", msg.UserID, "duration_ms", time.Since(started).Milliseconds(), "status", "failed", "error_code", code)
		if ctx.Err() != nil {
			return
		}
		_ = a.sendChunks(ctx, msg.ID, replyChannelID, "処理中にエラーが発生しました。時間をおいてもう一度試してください。")
		return
	}
	slog.Info("Discord request completed", "component", "discord_bot", "event", "request_completed", "request_id", request.RequestID, "discord_message_id", msg.ID, "guild_id", msg.GuildID, "channel_id", msg.ChannelID, "user_id", msg.UserID, "duration_ms", time.Since(started).Milliseconds(), "status", "success")
}

func (a *Adapter) StopAccepting() {
	if a != nil {
		a.mu.Lock()
		a.closed = true
		a.mu.Unlock()
	}
}

func (a *Adapter) Wait(ctx context.Context) error {
	if a == nil || ctx == nil {
		return errors.New("discordbot: adapter or context is nil")
	}
	done := make(chan struct{})
	go func() { a.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Adapter) sendChunks(ctx context.Context, replyTo, channelID, text string) error {
	for _, chunk := range splitMessage(text, a.maxChars) {
		if _, err := a.sendWithRetry(ctx, replyTo, channelID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) sendWithRetry(ctx context.Context, replyTo, channelID, content string) (SentMessage, error) {
	var err error
	for attempt := 0; attempt <= a.maxRetries; attempt++ {
		var sent SentMessage
		sent, err = a.sender.Reply(ctx, replyTo, channelID, content)
		if err == nil {
			if sent.ID == "" || sent.CreatedAt.IsZero() {
				return SentMessage{}, errors.New("discordbot: reply sender returned incomplete message")
			}
			return sent, nil
		}
		if attempt == a.maxRetries || !retryable(err) {
			return SentMessage{}, err
		}
		delay := time.Duration(1<<min(attempt, 2)) * time.Second
		var retryAfter interface{ RetryAfter() time.Duration }
		if errors.As(err, &retryAfter) && retryAfter.RetryAfter() > 0 {
			delay = retryAfter.RetryAfter()
		}
		if err := wait(ctx, delay); err != nil {
			return SentMessage{}, err
		}
	}
	return SentMessage{}, err
}

func retryable(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		code := status.StatusCode()
		return code == 429 || code >= 500
	}
	return false
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	valueCopy := value
	return &valueCopy
}
func hasMention(content, id string) bool {
	return strings.Contains(content, "<@"+id+">") || strings.Contains(content, "<@!"+id+">")
}
func removeMention(content, id string) string {
	content = strings.ReplaceAll(content, "<@"+id+">", "")
	content = strings.ReplaceAll(content, "<@!"+id+">", "")
	return strings.TrimSpace(content)
}

func normalizeInput(content string) (string, bool) {
	if !utf8.ValidString(content) {
		return "", false
	}
	content = strings.Map(func(r rune) rune {
		if r == 0 || (unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r') {
			return -1
		}
		return r
	}, content)
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > maxInputChars {
		return "", false
	}
	zeroWidth := 0
	for _, r := range content {
		if r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff {
			zeroWidth++
		}
	}
	if zeroWidth > 64 {
		return "", false
	}
	return content, true
}

func splitMessage(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" || limit < 1 {
		return nil
	}
	runes := []rune(text)
	var out []string
	for len(runes) > 0 {
		end := len(runes)
		if end > limit {
			end = limit
			cut := -1
			for _, sep := range []rune{'\n', ' ', '。', '！', '？', '.', '!', '?'} {
				for i := end - 1; i > 0; i-- {
					if runes[i] == sep {
						cut = i + 1
						break
					}
				}
				if cut > 0 {
					break
				}
			}
			if cut > 0 {
				end = cut
			}
		}
		part := strings.TrimSpace(string(runes[:end]))
		if part != "" {
			out = append(out, part)
		}
		runes = runes[end:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	return out
}

func (a *Adapter) String() string { return fmt.Sprintf("discordbot.Adapter(maxChars=%d)", a.maxChars) }
