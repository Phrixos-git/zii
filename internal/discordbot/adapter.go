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
	processor    Processor
	queue        *orchestrator.RequestQueue
	sender       ReplySender
	maxChars     int
	maxRetries   int
	outputGuard  outputGuard
	clock        func() time.Time
	newRequestID func() string
	wait         func(context.Context, time.Duration) error
	mu           sync.Mutex
	inFlight     map[string]struct{}
	limiters     map[string]userLimiter
	closed       bool
	active       sync.WaitGroup
}

type userLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type Config struct {
	ReplyMaxChars, SendMaxRetries int
	BlockedOutputValues           []string
	Clock                         func() time.Time
	RequestIDGenerator            func() string
	Wait                          func(context.Context, time.Duration) error
}

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
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.RequestIDGenerator == nil {
		cfg.RequestIDGenerator = uuid.NewString
	}
	if cfg.Wait == nil {
		cfg.Wait = wait
	}
	return &Adapter{processor: processor, queue: queue, sender: sender, maxChars: cfg.ReplyMaxChars, maxRetries: cfg.SendMaxRetries, outputGuard: newOutputGuard(cfg.BlockedOutputValues), clock: cfg.Clock, newRequestID: cfg.RequestIDGenerator, wait: cfg.Wait, inFlight: make(map[string]struct{}), limiters: make(map[string]userLimiter)}, nil
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
		msg.ReceivedAt = a.clock().UTC()
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
	entry.lastSeen = a.clock()
	a.limiters[msg.UserID] = entry
	if len(a.limiters) > 4096 {
		cutoff := a.clock().Add(-time.Minute)
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
	requestID := a.newRequestID()
	if strings.TrimSpace(requestID) == "" {
		slog.Error("Discord request ID generation failed", "component", "discord_bot", "event", "request_failed", "discord_message_id", msg.ID, "status", "failed", "error_code", "request_id_error")
		return
	}
	request := orchestrator.Request{RequestID: requestID, DiscordMessageID: msg.ID, GuildID: nullable(msg.GuildID), ChannelID: msg.ChannelID, ThreadID: msg.ThreadID, UserID: msg.UserID, Content: msg.Content, MessageCreatedAt: msg.CreatedAt.UTC(), ReceivedAt: msg.ReceivedAt.UTC()}
	replyChannelID := msg.ChannelID
	if msg.ThreadID != "" {
		replyChannelID = msg.ThreadID
	}
	started := a.clock()
	err := a.processor.ProcessQueuedWith(ctx, a.queue, request, func(workCtx context.Context, result orchestrator.Reply) error {
		if reason := a.outputGuard.reason(result.Content); reason != "" {
			slog.Error("Discord reply blocked by output security guard", "component", "discord_bot", "event", "unsafe_output_blocked", "request_id", request.RequestID, "discord_message_id", msg.ID, "status", "blocked", "error_code", reason)
			result.Content = unsafeOutputReply
		}
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
		if persistErr := a.processor.RecordSuccessfulReply(workCtx, result, orchestrator.DiscordReplyResult{RequestID: result.RequestID, Success: true, DiscordMessageID: first.ID, CreatedAt: first.CreatedAt}); persistErr != nil {
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
		var status interface{ StatusCode() int }
		if errors.As(err, &status) && status.StatusCode() == 429 {
			var retryAfter interface{ RetryAfter() time.Duration }
			if !errors.As(err, &retryAfter) || retryAfter.RetryAfter() <= 0 {
				return SentMessage{}, err
			}
			delay = retryAfter.RetryAfter()
		}
		var retryAfter interface{ RetryAfter() time.Duration }
		if errors.As(err, &retryAfter) && retryAfter.RetryAfter() > 0 {
			if status == nil || status.StatusCode() != 429 {
				delay = retryAfter.RetryAfter()
			}
		}
		if err := a.wait(ctx, delay); err != nil {
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
		if isZeroWidthOrFormat(r) {
			zeroWidth++
		}
	}
	if zeroWidth > 64 {
		return "", false
	}
	return content, true
}

func isZeroWidthOrFormat(r rune) bool {
	return (r >= 0x200b && r <= 0x200f) ||
		(r >= 0x202a && r <= 0x202e) ||
		(r >= 0x2060 && r <= 0x206f) ||
		r == 0x180e || r == 0xfeff
}

func splitMessage(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" || limit < 1 {
		return nil
	}
	runes := []rune(text)
	var out []string
	offset := 0
	prefix := ""
	for offset < len(runes) {
		capacity := limit - utf8.RuneCountInString(prefix)
		if capacity < 1 {
			prefix = ""
			capacity = limit
		}
		remaining := runes[offset:]
		end := chooseSplitCut(remaining, capacity)
		if end < 1 {
			end = min(len(remaining), capacity)
		}
		fence := markdownFenceAt(runes[:offset+end], offset+end == len(runes))
		suffix := ""
		nextPrefix := ""
		if fence != nil && offset+end < len(runes) {
			fenceClose := "\n" + fence.marker
			contentCapacity := capacity - utf8.RuneCountInString(fenceClose)
			if contentCapacity > 0 {
				end = chooseSplitCut(remaining, contentCapacity)
				if end < 1 {
					end = min(len(remaining), contentCapacity)
				}
				fence = markdownFenceAt(runes[:offset+end], false)
				if fence != nil && offset+end < len(runes) {
					suffix = "\n" + fence.marker
					nextPrefix = fence.opening + "\n"
				}
			}
		}
		part := prefix + string(remaining[:end]) + suffix
		if offset+end == len(runes) {
			if fence := markdownFenceAt(runes[:offset+end], true); fence != nil {
				closing := "\n" + fence.marker
				if utf8.RuneCountInString(part+closing) <= limit {
					part += closing
				}
			}
		}
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
		offset += end
		prefix = nextPrefix
	}
	return out
}

func chooseSplitCut(runes []rune, capacity int) int {
	if len(runes) <= capacity {
		return len(runes)
	}
	end := capacity
	for _, sep := range []rune{'\n', ' ', '。', '！', '？', '.', '!', '?'} {
		for i := end - 1; i > 0; i-- {
			if runes[i] == sep {
				return i + 1
			}
		}
	}
	return end
}

type markdownFence struct {
	marker  string
	opening string
}

func markdownFenceAt(runes []rune, includeFinalLine bool) *markdownFence {
	content := string(runes)
	lines := strings.Split(content, "\n")
	var open *markdownFence
	for i, line := range lines {
		if i == len(lines)-1 && !strings.HasSuffix(content, "\n") && !includeFinalLine {
			break
		}
		trimmed := strings.TrimLeft(line, " \t")
		if len(trimmed) < 3 || (trimmed[0] != '`' && trimmed[0] != '~') {
			continue
		}
		count := 0
		for count < len(trimmed) && trimmed[count] == trimmed[0] {
			count++
		}
		if count < 3 {
			continue
		}
		if open == nil {
			open = &markdownFence{marker: trimmed[:count], opening: trimmed}
			continue
		}
		if trimmed[0] == open.marker[0] && count >= len(open.marker) && strings.TrimSpace(trimmed[count:]) == "" {
			open = nil
		}
	}
	return open
}

func (a *Adapter) String() string { return fmt.Sprintf("discordbot.Adapter(maxChars=%d)", a.maxChars) }
