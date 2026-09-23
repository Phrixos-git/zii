package discordbot

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/Phrixos-git/zii/internal/orchestrator"
	"github.com/Phrixos-git/zii/internal/searchmcp"
	"github.com/Phrixos-git/zii/internal/storage"
)

type e2eChatClient struct {
	answer   string
	requests [][]chat.Message
}

func (c *e2eChatClient) Chat(_ context.Context, messages []chat.Message, _ []llm.ToolDefinition) (llm.Completion, error) {
	c.requests = append(c.requests, append([]chat.Message(nil), messages...))
	return llm.Completion{FinishReason: "stop", Message: chat.Message{Role: "assistant", Content: c.answer}}, nil
}

func (*e2eChatClient) CountTokens(_ context.Context, messages []chat.Message) (int, error) {
	total := 0
	for _, message := range messages {
		total += len([]rune(message.Content))
	}
	return total, nil
}

type e2eSearchClient struct{ calls int }

func (c *e2eSearchClient) Call(context.Context, string, json.RawMessage) (searchmcp.ToolResult, error) {
	c.calls++
	return searchmcp.ToolResult{}, fmt.Errorf("unexpected Search MCP call")
}
func (*e2eSearchClient) Reconnect(context.Context) error { return nil }

func TestDiscordSQLiteEndToEndLongReplyAndDuplicateEvent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "zii.db")
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository, err := storage.NewRepository(db)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	fixedNow := time.Now().UTC().Truncate(time.Second)
	replyAt := fixedNow.Add(time.Minute)
	answer := strings.TrimSpace(strings.Repeat("Zii answer. ", 180))
	chatClient := &e2eChatClient{answer: answer}
	registry, err := searchmcp.NewRegistry([]searchmcp.Tool{
		{Name: "search_local", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "search_web", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "fetch_page", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	search := &e2eSearchClient{}
	loop, err := orchestrator.NewToolLoop(chatClient, search, registry)
	if err != nil {
		t.Fatalf("NewToolLoop: %v", err)
	}
	service, err := orchestrator.NewService(repository, chatClient, loop, orchestrator.ServiceConfig{Clock: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	queue, err := orchestrator.NewRequestQueue(orchestrator.QueueConfig{})
	if err != nil {
		t.Fatalf("NewRequestQueue: %v", err)
	}
	t.Cleanup(func() { _ = queue.Shutdown(context.Background()) })
	sender := &senderFake{createdAt: replyAt}
	adapter, err := New(service, queue, sender, Config{Clock: func() time.Time { return fixedNow }, RequestIDGenerator: func() string { return "e2e-request" }})
	if err != nil {
		t.Fatalf("New Adapter: %v", err)
	}
	messageCreatedAt := fixedNow.Add(-time.Hour)
	incoming := Incoming{ID: "discord-question", ChannelID: "dm-channel", UserID: "user", Content: "answer at length", CreatedAt: messageCreatedAt, IsDM: true}
	adapter.Handle(ctx, incoming, "bot")
	if sender.calls != 2 || len(sender.replies) != 2 || len(sender.replyTo) != 2 || sender.replyTo[0] != incoming.ID || sender.replyTo[1] != incoming.ID {
		t.Fatalf("Discord sends=%d reply targets=%v; want 2 replies to source message", sender.calls, sender.replyTo)
	}
	if len(chatClient.requests) != 1 || chatClient.requests[0][0].Role != "system" || chatClient.requests[0][len(chatClient.requests[0])-1].Role != "user" {
		t.Fatalf("LLM request context roles are incorrect: %+v", chatClient.requests)
	}
	if search.calls != 0 {
		t.Fatalf("unexpected Search MCP calls: %d", search.calls)
	}

	rows, err := db.QueryContext(ctx, `SELECT role, discord_message_id, content, created_at FROM messages ORDER BY created_at`)
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()
	type storedMessage struct{ role, discordID, content, createdAt string }
	var got []storedMessage
	for rows.Next() {
		var row storedMessage
		if err := rows.Scan(&row.role, &row.discordID, &row.content, &row.createdAt); err != nil {
			t.Fatalf("scan message: %v", err)
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate messages: %v", err)
	}
	if len(got) != 2 || got[0].role != "user" || got[0].discordID != incoming.ID || got[0].content != incoming.Content || got[0].createdAt != messageCreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z") {
		t.Fatalf("stored user message = %+v", got)
	}
	if got[1].role != "assistant" || got[1].discordID != "reply-id" || got[1].content != answer || got[1].createdAt != replyAt.Format("2006-01-02T15:04:05.000000000Z") {
		t.Fatalf("stored assistant message = %+v", got[1])
	}
	var lastActive string
	if err := db.QueryRowContext(ctx, `SELECT last_active_at FROM conversations WHERE id = (SELECT conversation_id FROM messages WHERE discord_message_id = ?)`, incoming.ID).Scan(&lastActive); err != nil {
		t.Fatalf("query last_active_at: %v", err)
	}
	if lastActive != fixedNow.Format("2006-01-02T15:04:05.000000000Z") {
		t.Fatalf("last_active_at = %q; assistant reply must not update it", lastActive)
	}

	adapter.Handle(ctx, incoming, "bot")
	var messageCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if messageCount != 2 || sender.calls != 2 || len(chatClient.requests) != 1 {
		t.Fatalf("duplicate event caused side effects: messages=%d sends=%d LLM calls=%d", messageCount, sender.calls, len(chatClient.requests))
	}
}
