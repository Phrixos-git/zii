package discordbot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Phrixos-git/zii/internal/orchestrator"
)

type processorFake struct {
	request    orchestrator.Request
	reply      orchestrator.Reply
	processErr error
	saved      []orchestrator.DiscordReplyResult
}

func (p *processorFake) ProcessQueuedWith(ctx context.Context, _ *orchestrator.RequestQueue, r orchestrator.Request, complete func(context.Context, orchestrator.Reply) error) error {
	p.request = r
	if p.reply.RequestID == "" {
		p.reply = orchestrator.Reply{RequestID: r.RequestID, ReplyToMessageID: r.DiscordMessageID, Content: "answer"}
	}
	if p.processErr != nil {
		return p.processErr
	}
	return complete(ctx, p.reply)
}
func (p *processorFake) RecordSuccessfulReply(_ context.Context, _ orchestrator.Reply, r orchestrator.DiscordReplyResult) error {
	p.saved = append(p.saved, r)
	return nil
}

type senderFake struct {
	replies  []string
	channels []string
	failAt   int
	calls    int
}

func (s *senderFake) Reply(_ context.Context, replyTo, channel, content string) (SentMessage, error) {
	s.calls++
	if s.failAt == s.calls {
		return SentMessage{}, errors.New("send failed")
	}
	s.replies = append(s.replies, content)
	s.channels = append(s.channels, channel)
	return SentMessage{ID: "reply-id", CreatedAt: time.Now()}, nil
}
func testQueue(t *testing.T) *orchestrator.RequestQueue {
	t.Helper()
	q, err := orchestrator.NewRequestQueue(orchestrator.QueueConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
	return q
}

func TestHandleRequiresMentionInGuildAndStripsIt(t *testing.T) {
	p := &processorFake{}
	s := &senderFake{}
	a, err := New(p, testQueue(t), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	base := Incoming{ID: "m1", GuildID: "g", ChannelID: "c", UserID: "u", Content: "hello", CreatedAt: time.Now()}
	a.Handle(context.Background(), base, "bot")
	if p.request.RequestID != "" {
		t.Fatal("unmentioned guild message was processed")
	}
	base.Content = "  <@!bot>  tell me something  "
	a.Handle(context.Background(), base, "bot")
	if p.request.Content != "tell me something" || p.request.GuildID == nil || *p.request.GuildID != "g" {
		t.Fatalf("request = %+v", p.request)
	}
	if len(s.replies) != 1 || len(p.saved) != 1 {
		t.Fatalf("sent=%v saved=%v", s.replies, p.saved)
	}
}

func TestHandleKeepsThreadScopeAndRepliesInThread(t *testing.T) {
	p := &processorFake{}
	s := &senderFake{}
	a, _ := New(p, testQueue(t), s, Config{})
	a.Handle(context.Background(), Incoming{ID: "m", GuildID: "g", ChannelID: "parent", ThreadID: "thread", UserID: "u", Content: "<@bot> question", CreatedAt: time.Now()}, "bot")
	if p.request.ChannelID != "parent" || p.request.ThreadID != "thread" {
		t.Fatalf("request channel/thread=%q/%q", p.request.ChannelID, p.request.ThreadID)
	}
	if len(s.replies) != 1 || s.channels[0] != "thread" {
		t.Fatalf("replies=%v channels=%v", s.replies, s.channels)
	}
}

func TestHandleDMAndRejectsBotsWebhookAndInvalidInput(t *testing.T) {
	p := &processorFake{}
	s := &senderFake{}
	a, _ := New(p, testQueue(t), s, Config{})
	m := Incoming{ID: "dm", ChannelID: "dmch", UserID: "u", Content: "question", CreatedAt: time.Now(), IsDM: true}
	a.Handle(context.Background(), m, "bot")
	if p.request.GuildID != nil || p.request.Content != "question" {
		t.Fatalf("DM request=%+v", p.request)
	}
	count := len(s.replies)
	m.ID = "bot"
	m.AuthorBot = true
	a.Handle(context.Background(), m, "bot")
	m.AuthorBot = false
	m.Webhook = true
	a.Handle(context.Background(), m, "bot")
	m.Webhook = false
	m.Content = strings.Repeat("x", maxInputChars+1)
	a.Handle(context.Background(), m, "bot")
	if len(s.replies) != count {
		t.Fatal("rejected event was replied to")
	}
}

func TestHandleDoesNotSendErrorReplyAfterCancellation(t *testing.T) {
	p := &processorFake{processErr: errors.New("request failed")}
	s := &senderFake{}
	a, _ := New(p, testQueue(t), s, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.Handle(ctx, Incoming{ID: "m", ChannelID: "c", UserID: "u", Content: "q", IsDM: true, CreatedAt: time.Now()}, "bot")
	if len(s.replies) != 0 {
		t.Fatalf("replies after cancellation = %v", s.replies)
	}
}

func TestLongReplySavesFirstReplyOnlyAfterAllChunksSucceed(t *testing.T) {
	p := &processorFake{reply: orchestrator.Reply{RequestID: "r", ReplyToMessageID: "q", Content: strings.Repeat("a", 2000)}}
	s := &senderFake{}
	a, _ := New(p, testQueue(t), s, Config{})
	a.Handle(context.Background(), Incoming{ID: "q", ChannelID: "ch", UserID: "u", Content: "ask", IsDM: true, CreatedAt: time.Now()}, "bot")
	if len(s.replies) != 2 || len(s.replies[0]) > defaultReplyMaxChars || len(s.replies[1]) > defaultReplyMaxChars || len(p.saved) != 1 || p.saved[0].DiscordMessageID != "reply-id" {
		t.Fatalf("chunks=%d saved=%+v", len(s.replies), p.saved)
	}
	p.saved = nil
	s = &senderFake{failAt: 2}
	a, _ = New(p, testQueue(t), s, Config{})
	a.Handle(context.Background(), Incoming{ID: "q2", ChannelID: "ch", UserID: "u", Content: "ask", IsDM: true, CreatedAt: time.Now()}, "bot")
	if len(p.saved) != 0 {
		t.Fatal("partial reply was persisted")
	}
}

func TestSplitMessagePrefersNewlinesAndRuneLimits(t *testing.T) {
	parts := splitMessage(strings.Repeat("界", 10)+"\n"+strings.Repeat("x", 10), 10)
	if len(parts) != 2 || len([]rune(parts[0])) > 10 || !strings.Contains(parts[0], "界") {
		t.Fatalf("parts=%q", parts)
	}
}
