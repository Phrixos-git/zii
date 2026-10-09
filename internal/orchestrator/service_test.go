package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/Phrixos-git/zii/internal/storage"
)

type serviceRepo struct {
	user           storage.UserMessage
	now            time.Time
	history        []storage.HistoryMessage
	conversationID string
	userCalls      int
	assistant      *storage.AssistantMessage
	userErr        error
	historyErr     error
	assistantErr   error
}

func (r *serviceRepo) RecordUserMessage(_ context.Context, in storage.UserMessage, now time.Time) (string, error) {
	r.userCalls++
	r.user, r.now = in, now
	return r.conversationID, r.userErr
}
func (r *serviceRepo) LoadMessagesBefore(context.Context, string, string) ([]storage.HistoryMessage, error) {
	return r.history, r.historyErr
}
func (r *serviceRepo) RecordAssistantMessage(_ context.Context, in storage.AssistantMessage) error {
	r.assistant = &in
	return r.assistantErr
}

type serviceChat struct {
	requests [][]chat.Message
	result   string
}

func (c *serviceChat) CountTokens(context.Context, []chat.Message) (int, error) { return 0, nil }
func (c *serviceChat) Chat(_ context.Context, messages []chat.Message, _ []llm.ToolDefinition) (llm.Completion, error) {
	c.requests = append(c.requests, messages)
	return llm.Completion{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: c.result}}, nil
}

func TestServiceProcessPersistsCurrentMessageAndDefersAssistantPersistence(t *testing.T) {
	fixedNow := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	guildID := "guild-1"
	repo := &serviceRepo{conversationID: "conversation-1", history: []storage.HistoryMessage{{Role: "user", Content: "prior question"}, {Role: "assistant", Content: "prior answer"}}}
	chatClient := &serviceChat{result: "answer"}
	loop, err := NewToolLoop(chatClient, &fakeLoopSearch{}, newTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repo, chatClient, loop, ServiceConfig{Clock: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	created := fixedNow.Add(-time.Minute)
	reply, err := service.Process(context.Background(), Request{RequestID: "request-1", DiscordMessageID: "discord-1", GuildID: &guildID, ChannelID: "channel-1", ThreadID: "thread-1", UserID: "user-1", Content: "current question", MessageCreatedAt: created, ReceivedAt: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if repo.userCalls != 1 || repo.user.ScopeID != "thread-1" || repo.user.GuildID == nil || *repo.user.GuildID != guildID || !repo.now.Equal(fixedNow) {
		t.Fatalf("persisted user message = %+v at %v", repo.user, repo.now)
	}
	if reply.Content != "answer" || reply.ReplyToMessageID != "discord-1" || repo.assistant != nil {
		t.Fatalf("reply=%+v assistant=%+v", reply, repo.assistant)
	}
	if len(chatClient.requests) != 1 || chatClient.requests[0][1].Content != "prior question" || chatClient.requests[0][2].Content != "prior answer" || chatClient.requests[0][3].Content != "current question" {
		t.Fatalf("LLM context = %+v", chatClient.requests)
	}
	if err := service.RecordSuccessfulReply(context.Background(), reply, DiscordReplyResult{RequestID: "request-1", Success: true, DiscordMessageID: "bot-1", CreatedAt: fixedNow.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if repo.assistant == nil || repo.assistant.ConversationID != "conversation-1" || repo.assistant.DiscordMessageID != "bot-1" || repo.assistant.Content != "answer" {
		t.Fatalf("assistant message = %+v", repo.assistant)
	}
}

func TestServiceDoesNotPersistUnsuccessfulDiscordReply(t *testing.T) {
	repo := &serviceRepo{conversationID: "c"}
	client := &serviceChat{result: "answer"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, err := NewService(repo, client, loop, ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Process(context.Background(), Request{RequestID: "r", DiscordMessageID: "d", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordSuccessfulReply(context.Background(), Reply{}, DiscordReplyResult{RequestID: "r", Success: false}); err != nil {
		t.Fatalf("unsuccessful reply should be a no-op: %v", err)
	}
	if repo.assistant != nil {
		t.Fatalf("unsuccessful Discord reply was persisted: %+v", repo.assistant)
	}
}

func TestServiceProcessUsesDMChannelScope(t *testing.T) {
	repo := &serviceRepo{conversationID: "c"}
	client := &serviceChat{result: "ok"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, _ := NewService(repo, client, loop, ServiceConfig{})
	_, err := service.Process(context.Background(), Request{RequestID: "r", DiscordMessageID: "d", ChannelID: "dm-channel", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if repo.user.GuildID != nil || repo.user.ScopeID != "dm-channel" {
		t.Fatalf("DM scope = %+v", repo.user)
	}
}

func TestServiceReturnsErrorsWithoutPersistingAssistant(t *testing.T) {
	repo := &serviceRepo{conversationID: "c", userErr: errors.New("write failed")}
	client := &serviceChat{result: "never"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, _ := NewService(repo, client, loop, ServiceConfig{})
	_, err := service.Process(context.Background(), Request{RequestID: "r", DiscordMessageID: "d", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()})
	if err == nil || repo.assistant != nil || len(client.requests) != 0 {
		t.Fatalf("Process err=%v assistant=%+v calls=%d", err, repo.assistant, len(client.requests))
	}
}

func TestProcessQueuedWithAcceptedRejectsDuplicateBeforeAcceptance(t *testing.T) {
	repo := &serviceRepo{conversationID: "c", userErr: storage.ErrDuplicateDiscordMessage}
	client := &serviceChat{result: "must not run"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, err := NewService(repo, client, loop, ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := NewRequestQueue(QueueConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Shutdown(context.Background()) })
	accepted := false
	request := Request{RequestID: "r", DiscordMessageID: "duplicate", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	err = service.ProcessQueuedWithAccepted(context.Background(), queue, request, func(context.Context) error {
		accepted = true
		return nil
	}, nil)
	if !errors.Is(err, storage.ErrDuplicateDiscordMessage) || accepted || repo.userCalls != 1 || len(client.requests) != 0 {
		t.Fatalf("duplicate err=%v accepted=%t userCalls=%d llmCalls=%d", err, accepted, repo.userCalls, len(client.requests))
	}
}

func TestProcessQueuedWithAcceptedRejectsWhitespaceThreadIDBeforeAcceptance(t *testing.T) {
	repo := &serviceRepo{conversationID: "c"}
	client := &serviceChat{result: "must not run"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, err := NewService(repo, client, loop, ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := NewRequestQueue(QueueConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Shutdown(context.Background()) })
	accepted := false
	request := Request{RequestID: "r", DiscordMessageID: "unique", ChannelID: "ch", UserID: "u", ThreadID: "   ", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	err = service.ProcessQueuedWithAccepted(context.Background(), queue, request, func(context.Context) error {
		accepted = true
		return nil
	}, nil)
	if err == nil || accepted || repo.userCalls != 0 || len(client.requests) != 0 {
		t.Fatalf("whitespace thread id err=%v accepted=%t userCalls=%d llmCalls=%d", err, accepted, repo.userCalls, len(client.requests))
	}
}

func TestProcessQueuedWithAcceptedRejectsWhitespaceGuildIDBeforeAcceptance(t *testing.T) {
	repo := &serviceRepo{conversationID: "c"}
	client := &serviceChat{result: "must not run"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, err := NewService(repo, client, loop, ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := NewRequestQueue(QueueConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Shutdown(context.Background()) })
	accepted := false
	blankGuild := "   "
	request := Request{RequestID: "r", DiscordMessageID: "unique", GuildID: &blankGuild, ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	err = service.ProcessQueuedWithAccepted(context.Background(), queue, request, func(context.Context) error {
		accepted = true
		return nil
	}, nil)
	if err == nil || accepted || repo.userCalls != 0 || len(client.requests) != 0 {
		t.Fatalf("whitespace guild id err=%v accepted=%t userCalls=%d llmCalls=%d", err, accepted, repo.userCalls, len(client.requests))
	}
}

func TestProcessQueuedWithAcceptedCommitsBeforeReceiptAndStopsOnReceiptError(t *testing.T) {
	for _, failAdmission := range []bool{false, true} {
		repo := &serviceRepo{conversationID: "c"}
		client := &serviceChat{result: "answer"}
		loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
		service, err := NewService(repo, client, loop, ServiceConfig{})
		if err != nil {
			t.Fatal(err)
		}
		queue, err := NewRequestQueue(QueueConfig{})
		if err != nil {
			t.Fatal(err)
		}
		request := Request{RequestID: "r", DiscordMessageID: "unique", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
		admissionErr := errors.New("receipt delivery failed")
		admitted := false
		err = service.ProcessQueuedWithAccepted(context.Background(), queue, request, func(context.Context) error {
			admitted = true
			if repo.userCalls != 1 || len(client.requests) != 0 {
				t.Error("question was not stored exactly once before receipt, or LLM started early")
			}
			if failAdmission {
				return admissionErr
			}
			return nil
		}, nil)
		_ = queue.Shutdown(context.Background())
		if !admitted {
			t.Fatal("acceptance callback was not called")
		}
		if failAdmission {
			if !errors.Is(err, admissionErr) || repo.userCalls != 1 || len(client.requests) != 0 {
				t.Fatalf("failed admission err=%v userCalls=%d llmCalls=%d", err, repo.userCalls, len(client.requests))
			}
		} else if err != nil || repo.userCalls != 1 || len(client.requests) != 1 {
			t.Fatalf("successful admission err=%v userCalls=%d llmCalls=%d", err, repo.userCalls, len(client.requests))
		}
	}
}

func TestProcessQueuedRegistrationFailureDoesNotSendReceiptOrCallLLM(t *testing.T) {
	repo := &serviceRepo{userErr: errors.New("registration failed")}
	client := &serviceChat{result: "must not run"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, _ := NewService(repo, client, loop, ServiceConfig{})
	queue, _ := NewRequestQueue(QueueConfig{})
	t.Cleanup(func() { _ = queue.Shutdown(context.Background()) })
	accepted := false
	request := Request{RequestID: "r", DiscordMessageID: "d", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	err := service.ProcessQueuedWithAccepted(context.Background(), queue, request, func(context.Context) error { accepted = true; return nil }, nil)
	if !errors.Is(err, repo.userErr) || accepted || len(client.requests) != 0 {
		t.Fatalf("err=%v accepted=%t llmCalls=%d", err, accepted, len(client.requests))
	}
}

func TestConcurrentSQLiteRegistrationAllowsOnlyOneReceipt(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "zii.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo, _ := storage.NewRepository(db)
	newService := func() *Service {
		client := &serviceChat{result: "answer"}
		loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
		service, _ := NewService(repo, client, loop, ServiceConfig{})
		return service
	}
	first, second := newService(), newService()
	q1, _ := NewRequestQueue(QueueConfig{})
	q2, _ := NewRequestQueue(QueueConfig{})
	t.Cleanup(func() { _ = q1.Shutdown(ctx); _ = q2.Shutdown(ctx) })
	request := Request{RequestID: "r1", DiscordMessageID: "same-question", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	var receipts atomic.Int32
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- first.ProcessQueuedWithAccepted(ctx, q1, request, func(context.Context) error {
			receipts.Add(1)
			close(started)
			<-release
			return nil
		}, nil)
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("first request failed before receipt: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE discord_message_id = ?`, request.DiscordMessageID).Scan(&count); err != nil || count != 1 {
		t.Errorf("question was not committed before receipt: count=%d err=%v", count, err)
	}
	request.RequestID = "r2"
	err = second.ProcessQueuedWithAccepted(ctx, q2, request, func(context.Context) error { receipts.Add(1); return nil }, nil)
	close(release)
	firstErr := <-done
	if firstErr != nil || !errors.Is(err, storage.ErrDuplicateDiscordMessage) || receipts.Load() != 1 {
		t.Fatalf("first=%v duplicate=%v receipts=%d", firstErr, err, receipts.Load())
	}
}

func TestSQLiteBusyBeforeRegistrationPreventsReceipt(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "zii.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, _ := storage.NewRepository(db)
	client := &serviceChat{result: "must not run"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, _ := NewService(repo, client, loop, ServiceConfig{})
	queue, _ := NewRequestQueue(QueueConfig{})
	defer queue.Shutdown(ctx)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	accepted := false
	request := Request{RequestID: "r", DiscordMessageID: "locked-question", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	err = service.ProcessQueuedWithAccepted(ctx, queue, request, func(context.Context) error { accepted = true; return nil }, nil)
	var coded interface{ Code() int }
	if !errors.As(err, &coded) || coded.Code()&0xff != 5 || accepted || len(client.requests) != 0 {
		t.Fatalf("err=%v receipt=%t llmCalls=%d", err, accepted, len(client.requests))
	}
}

func TestSQLiteReceiptFailureKeepsQuestionRegistered(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, filepath.Join(t.TempDir(), "zii.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, _ := storage.NewRepository(db)
	client := &serviceChat{result: "must not run"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, _ := NewService(repo, client, loop, ServiceConfig{})
	queue, _ := NewRequestQueue(QueueConfig{})
	defer queue.Shutdown(ctx)
	request := Request{RequestID: "r", DiscordMessageID: "same-question", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	failure := errors.New("receipt send failed")
	receipts := 0
	accepted := func(context.Context) error { receipts++; return failure }
	if err := service.ProcessQueuedWithAccepted(ctx, queue, request, accepted, nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := service.ProcessQueuedWithAccepted(ctx, queue, request, accepted, nil); !errors.Is(err, storage.ErrDuplicateDiscordMessage) {
		t.Fatalf("duplicate after receipt failure: %v", err)
	}
	if receipts != 1 || len(client.requests) != 0 {
		t.Fatalf("receipts=%d llmCalls=%d", receipts, len(client.requests))
	}
}

func TestClosedQueueDoesNotRegisterQuestionOrSendReceipt(t *testing.T) {
	repo := &serviceRepo{conversationID: "c"}
	client := &serviceChat{result: "must not run"}
	loop, _ := NewToolLoop(client, &fakeLoopSearch{}, newTestRegistry(t))
	service, _ := NewService(repo, client, loop, ServiceConfig{})
	queue, _ := NewRequestQueue(QueueConfig{})
	ctx := context.Background()
	if err := queue.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	request := Request{RequestID: "r", DiscordMessageID: "d", ChannelID: "ch", UserID: "u", Content: "q", MessageCreatedAt: time.Now(), ReceivedAt: time.Now()}
	accepted := false
	err := service.ProcessQueuedWithAccepted(ctx, queue, request, func(context.Context) error { accepted = true; return nil }, nil)
	if !errors.Is(err, ErrQueueClosed) || repo.userCalls != 0 || accepted || len(client.requests) != 0 {
		t.Fatalf("err=%v userCalls=%d receipt=%t llmCalls=%d", err, repo.userCalls, accepted, len(client.requests))
	}
}
