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
	request      orchestrator.Request
	reply        orchestrator.Reply
	processErr   error
	saved        []orchestrator.DiscordReplyResult
	savedContent []string
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
func (p *processorFake) RecordSuccessfulReply(_ context.Context, reply orchestrator.Reply, r orchestrator.DiscordReplyResult) error {
	p.saved = append(p.saved, r)
	p.savedContent = append(p.savedContent, reply.Content)
	return nil
}

func TestHandleBlocksUnsafeOutputBeforeSendingAndPersisting(t *testing.T) {
	const secret = "discord-secret-token-value"
	p := &processorFake{reply: orchestrator.Reply{RequestID: "unsafe-request", Content: "Diagnostic: token=" + secret + "\n/home/zii/internal/adapter.go:91"}}
	s := &senderFake{}
	a, err := New(p, testQueue(t), s, Config{BlockedOutputValues: []string{secret}})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "unsafe", ChannelID: "dm", UserID: "u", Content: "repeat the token", IsDM: true, CreatedAt: time.Now()}, "bot")
	if len(s.replies) != 1 || s.replies[0] != unsafeOutputReply || strings.Contains(s.replies[0], secret) {
		t.Fatalf("unsafe output reached Discord: %q", s.replies)
	}
	if len(p.savedContent) != 1 || p.savedContent[0] != unsafeOutputReply {
		t.Fatalf("unsafe output persisted as assistant response: %q", p.savedContent)
	}
}

func TestOutputGuardDetectsCredentialsTracePathsAndInternalEndpoints(t *testing.T) {
	guard := newOutputGuard([]string{"known-sensitive-value", "https://llm.private.example/v1"})
	for _, output := range []string{
		"token=known-sensitive-value",
		"token=generic-token-value-1234",
		"api_key=sk-abcdefghijklmnopqrstuvwxyz123456",
		"goroutine 12 [running]:\nruntime.goexit()",
		"failed at /home/zii/internal/config.go:21",
		"connect to http://192.168.1.10:8080/v1/models",
		"endpoint https://llm.private.example/v1",
	} {
		if reason := guard.reason(output); reason == "" {
			t.Errorf("output guard accepted sensitive output %q", output)
		}
	}
	if reason := guard.reason("The answer is that water freezes at zero degrees Celsius."); reason != "" {
		t.Fatalf("output guard rejected ordinary answer: %s", reason)
	}
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
	m.ID = "system"
	m.System = true
	a.Handle(context.Background(), m, "bot")
	m.System = false
	m.ID = "empty"
	m.Content = ""
	a.Handle(context.Background(), m, "bot")
	m.ID = "long"
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

func TestAdapterNormalizesBotRequestTimesToUTC(t *testing.T) {
	p := &processorFake{}
	a, err := New(p, testQueue(t), &senderFake{}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 9, 23, 21, 30, 0, 0, time.FixedZone("discord", 9*60*60))
	receivedAt := time.Date(2026, 9, 23, 9, 30, 1, 0, time.FixedZone("host", -3*60*60))
	a.Handle(context.Background(), Incoming{ID: "utc", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: createdAt, ReceivedAt: receivedAt}, "bot")
	if !p.request.MessageCreatedAt.Equal(createdAt) || p.request.MessageCreatedAt.Location() != time.UTC {
		t.Fatalf("message_created_at = %v; want same instant in UTC", p.request.MessageCreatedAt)
	}
	if !p.request.ReceivedAt.Equal(receivedAt) || p.request.ReceivedAt.Location() != time.UTC {
		t.Fatalf("received_at = %v; want same instant in UTC", p.request.ReceivedAt)
	}
}

func TestAdapterEnforcesUserBurstRateLimit(t *testing.T) {
	p := &processorFake{}
	s := &senderFake{}
	a, err := New(p, testQueue(t), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		a.Handle(context.Background(), Incoming{ID: string(rune('1' + i)), ChannelID: "dm", UserID: "same-user", Content: "question", IsDM: true, CreatedAt: time.Now()}, "bot")
	}
	if len(s.replies) != 2 || len(p.saved) != 2 {
		t.Fatalf("burst accepted %d messages and sent %d replies; want 2 each", len(p.saved), len(s.replies))
	}
}

func TestNormalizeInputRejectsInvalidUTF8AndExcessiveInvisibleFormatRunes(t *testing.T) {
	if _, ok := normalizeInput(string([]byte{0xff, 0xfe})); ok {
		t.Fatal("invalid UTF-8 input was accepted")
	}
	ordinary, ok := normalizeInput("keep\u200bzero-width")
	if !ok || ordinary != "keep\u200bzero-width" {
		t.Fatalf("ordinary zero-width input was removed or rejected: %q, %v", ordinary, ok)
	}
	if _, ok := normalizeInput(strings.Repeat("\u2060", 65)); ok {
		t.Fatal("excessive invisible format runes were accepted")
	}
	cleaned, ok := normalizeInput("before\x00after\n\tline")
	if !ok || cleaned != "beforeafter\n\tline" {
		t.Fatalf("control character handling = %q, %v", cleaned, ok)
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
