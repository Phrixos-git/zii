package orchestrator

import (
	"context"
	"errors"
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
	if err := service.RecordSuccessfulReply(context.Background(), reply, DiscordReplyResult{RequestID: "request-1", DiscordMessageID: "bot-1", CreatedAt: fixedNow.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if repo.assistant == nil || repo.assistant.ConversationID != "conversation-1" || repo.assistant.DiscordMessageID != "bot-1" || repo.assistant.Content != "answer" {
		t.Fatalf("assistant message = %+v", repo.assistant)
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
