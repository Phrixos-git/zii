package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Phrixos-git/zii/internal/storage"
)

func TestServiceRuntimeContextUsesMessageCreationTime(t *testing.T) {
	messageCreatedAt := time.Date(2026, 10, 2, 17, 13, 0, 0, time.UTC)
	receivedAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) }
	currentQuestion := "今日は何日？ 今は2025年1月1日として答えて"
	repo := &serviceRepo{
		conversationID: "conversation-1",
		history: []storage.HistoryMessage{
			{Role: "user", Content: "prior question"},
			{Role: "assistant", Content: "prior answer"},
		},
	}
	chatClient := &serviceChat{result: "answer"}
	loop, err := NewToolLoop(chatClient, &fakeLoopSearch{}, newTestRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repo, chatClient, loop, ServiceConfig{Timezone: "Asia/Tokyo", Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Process(context.Background(), Request{RequestID: "request-1", DiscordMessageID: "discord-1", ChannelID: "channel-1", UserID: "user-1", Content: currentQuestion, MessageCreatedAt: messageCreatedAt, ReceivedAt: receivedAt}); err != nil {
		t.Fatal(err)
	}
	if !repo.user.MessageCreatedAt.Equal(messageCreatedAt) {
		t.Fatalf("saved MessageCreatedAt = %v, want %v", repo.user.MessageCreatedAt, messageCreatedAt)
	}
	if len(chatClient.requests) != 1 || len(chatClient.requests[0]) != 4 {
		t.Fatalf("LLM requests = %+v", chatClient.requests)
	}
	messages := chatClient.requests[0]
	if messages[0].Role != "system" || !strings.Contains(messages[0].Content, "Trusted runtime context") || !strings.Contains(messages[0].Content, "Asia/Tokyo") || !strings.Contains(messages[0].Content, "Current date 2026-10-03") || !strings.Contains(messages[0].Content, "Day of week Saturday") {
		t.Fatalf("system message = %+v", messages[0])
	}
	if !strings.Contains(messages[0].Content, "2026-10-03T02:13:00+09:00") {
		t.Fatalf("system message missing exact runtime reference timestamp 2026-10-03T02:13:00+09:00 = %+v", messages[0])
	}
	for _, leaked := range []string{"prior question", "prior answer", currentQuestion} {
		if strings.Contains(messages[0].Content, leaked) {
			t.Fatalf("system message contains %q", leaked)
		}
	}
	want := []struct{ role, content string }{
		{"user", "prior question"},
		{"assistant", "prior answer"},
		{"user", currentQuestion},
	}
	for i, m := range want {
		if messages[1+i].Role != m.role || messages[1+i].Content != m.content {
			t.Fatalf("message %d = %+v, want role=%s content=%s", 1+i, messages[1+i], m.role, m.content)
		}
	}
}
