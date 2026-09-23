package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Phrixos-git/zii/internal/storage"
)

// Repository is the persistence boundary used by the Orchestrator.
type Repository interface {
	RecordUserMessage(context.Context, storage.UserMessage, time.Time) (string, error)
	LoadMessagesBefore(context.Context, string, string) ([]storage.HistoryMessage, error)
	RecordAssistantMessage(context.Context, storage.AssistantMessage) error
}

// Request contains Discord-derived data required by OR-01 and DB-04.
type Request struct {
	RequestID        string
	DiscordMessageID string
	GuildID          *string
	ChannelID        string
	ThreadID         string
	UserID           string
	Content          string
	MessageCreatedAt time.Time
	ReceivedAt       time.Time
}

// Reply is the final answer prepared for the Discord adapter. ConversationID
// is intentionally hidden from the adapter and is used only for persistence.
type Reply struct {
	RequestID        string
	ReplyToMessageID string
	Content          string
	conversationID   string
}

// DiscordReplyResult is returned only after a successful Discord send.
type DiscordReplyResult struct {
	RequestID        string
	Success          bool
	DiscordMessageID string
	CreatedAt        time.Time
}

// Service coordinates persistence, history construction, and the tool loop.
type Service struct {
	repository Repository
	context    *ContextBuilder
	toolLoop   *ToolLoop
	system     string
	clock      func() time.Time
}

type ServiceConfig struct {
	SystemPrompt     string
	MaxHistoryTurns  int
	MaxHistoryTokens int
	Clock            func() time.Time
}

func NewService(repository Repository, client ChatClient, toolLoop *ToolLoop, cfg ServiceConfig) (*Service, error) {
	if repository == nil {
		return nil, errors.New("orchestrator: repository is nil")
	}
	if client == nil {
		return nil, errors.New("orchestrator: LLM client is nil")
	}
	if toolLoop == nil {
		return nil, errors.New("orchestrator: tool loop is nil")
	}
	maxTurns := cfg.MaxHistoryTurns
	if maxTurns == 0 {
		maxTurns = maxConversationTurns
	}
	maxTokens := cfg.MaxHistoryTokens
	if maxTokens == 0 {
		maxTokens = maxHistoryTokenLimit
	}
	contextBuilder, err := NewContextBuilder(client, maxTurns, maxTokens)
	if err != nil {
		return nil, err
	}
	systemPrompt := cfg.SystemPrompt
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = defaultSystemPrompt
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{repository: repository, context: contextBuilder, toolLoop: toolLoop, system: systemPrompt, clock: clock}, nil
}

// Process persists the accepted user message, builds its context, and returns
// a final answer for the Discord adapter. Assistant history is not saved here.
func (s *Service) Process(ctx context.Context, request Request) (Reply, error) {
	if s == nil || s.repository == nil || s.context == nil || s.toolLoop == nil {
		return Reply{}, errors.New("orchestrator: service is not initialized")
	}
	if ctx == nil {
		return Reply{}, errors.New("orchestrator: context is nil")
	}
	if err := validateRequest(request); err != nil {
		return Reply{}, err
	}
	scopeID := request.ChannelID
	if strings.TrimSpace(request.ThreadID) != "" {
		scopeID = request.ThreadID
	}
	now := s.clock().UTC()
	conversationID, err := s.repository.RecordUserMessage(ctx, storage.UserMessage{
		GuildID:          request.GuildID,
		ScopeID:          scopeID,
		UserID:           request.UserID,
		DiscordMessageID: request.DiscordMessageID,
		Content:          request.Content,
		MessageCreatedAt: request.MessageCreatedAt,
	}, now)
	if err != nil {
		return Reply{}, fmt.Errorf("orchestrator: persist user message: %w", err)
	}
	history, err := s.repository.LoadMessagesBefore(ctx, conversationID, request.DiscordMessageID)
	if err != nil {
		return Reply{}, fmt.Errorf("orchestrator: load conversation history: %w", err)
	}
	messages, err := s.context.Build(ctx, s.system, request.Content, history)
	if err != nil {
		return Reply{}, err
	}
	answer, err := s.toolLoop.Run(ctx, messages)
	if err != nil {
		return Reply{}, fmt.Errorf("orchestrator: generate answer: %w", err)
	}
	return Reply{
		RequestID:        request.RequestID,
		ReplyToMessageID: request.DiscordMessageID,
		Content:          answer,
		conversationID:   conversationID,
	}, nil
}

// ProcessQueued submits work using the stable conversation key components
// available before SQLite resolves the active conversation row.
func (s *Service) ProcessQueued(ctx context.Context, queue *RequestQueue, request Request) (Reply, error) {
	var reply Reply
	err := s.ProcessQueuedWith(ctx, queue, request, func(_ context.Context, result Reply) error { reply = result; return nil })
	return reply, err
}

// ProcessQueuedWith holds the conversation key until complete returns, so
// reply delivery and assistant persistence remain ordered with later turns.
func (s *Service) ProcessQueuedWith(ctx context.Context, queue *RequestQueue, request Request, complete func(context.Context, Reply) error) error {
	if queue == nil {
		return errors.New("orchestrator: request queue is nil")
	}
	scopeID := request.ChannelID
	if strings.TrimSpace(request.ThreadID) != "" {
		scopeID = request.ThreadID
	}
	guildID := "<dm>"
	if request.GuildID != nil {
		guildID = *request.GuildID
	}
	key := fmt.Sprintf("%d:%s%d:%s%d:%s", len(guildID), guildID, len(scopeID), scopeID, len(request.UserID), request.UserID)
	err := queue.Submit(ctx, key, func(workCtx context.Context) error {
		workCtx = withRequestID(workCtx, request.RequestID)
		reply, err := s.Process(workCtx, request)
		if err != nil {
			return err
		}
		if complete != nil {
			return complete(workCtx, reply)
		}
		return nil
	})
	return err
}

// RecordSuccessfulReply persists the answer only after the adapter confirms a
// successful Discord Reply and provides its message ID and creation time.
func (s *Service) RecordSuccessfulReply(ctx context.Context, reply Reply, result DiscordReplyResult) error {
	if s == nil || s.repository == nil {
		return errors.New("orchestrator: service is not initialized")
	}
	if ctx == nil {
		return errors.New("orchestrator: context is nil")
	}
	if !result.Success {
		return nil
	}
	if reply.conversationID == "" || strings.TrimSpace(reply.RequestID) == "" || strings.TrimSpace(reply.Content) == "" {
		return errors.New("orchestrator: pending reply is incomplete")
	}
	if result.RequestID != reply.RequestID {
		return errors.New("orchestrator: Discord reply request id does not match")
	}
	if strings.TrimSpace(result.DiscordMessageID) == "" || result.CreatedAt.IsZero() {
		return errors.New("orchestrator: successful Discord reply is missing message id or creation time")
	}
	if err := s.repository.RecordAssistantMessage(ctx, storage.AssistantMessage{
		ConversationID:   reply.conversationID,
		DiscordMessageID: result.DiscordMessageID,
		Content:          reply.Content,
		MessageCreatedAt: result.CreatedAt,
	}); err != nil {
		return fmt.Errorf("orchestrator: persist assistant reply: %w", err)
	}
	return nil
}

func validateRequest(request Request) error {
	for name, value := range map[string]string{
		"request id":         request.RequestID,
		"Discord message id": request.DiscordMessageID,
		"channel id":         request.ChannelID,
		"user id":            request.UserID,
		"content":            request.Content,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("orchestrator: %s is empty", name)
		}
	}
	if request.GuildID != nil && strings.TrimSpace(*request.GuildID) == "" {
		return errors.New("orchestrator: guild id is empty")
	}
	if request.ThreadID != "" && strings.TrimSpace(request.ThreadID) == "" {
		return errors.New("orchestrator: thread id is empty")
	}
	if request.MessageCreatedAt.IsZero() {
		return errors.New("orchestrator: Discord message creation time is zero")
	}
	if request.ReceivedAt.IsZero() {
		return errors.New("orchestrator: received time is zero")
	}
	return nil
}
