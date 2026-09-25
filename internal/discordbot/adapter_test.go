package discordbot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Phrixos-git/zii/internal/orchestrator"
	"github.com/Phrixos-git/zii/internal/storage"
)

type processorFake struct {
	request      orchestrator.Request
	reply        orchestrator.Reply
	admissionErr error
	processErr   error
	processed    int
	saved        []orchestrator.DiscordReplyResult
	savedContent []string
}

func (p *processorFake) ProcessQueuedWithAccepted(ctx context.Context, queue *orchestrator.RequestQueue, r orchestrator.Request, accepted func(context.Context) error, complete func(context.Context, orchestrator.Reply) error) error {
	p.request = r
	if p.admissionErr != nil {
		return p.admissionErr
	}
	if p.reply.RequestID == "" {
		p.reply = orchestrator.Reply{RequestID: r.RequestID, ReplyToMessageID: r.DiscordMessageID, Content: "answer"}
	}
	key := r.ChannelID + ":" + r.UserID
	return queue.SubmitWithAccepted(ctx, key, accepted, func(workCtx context.Context) error {
		p.processed++
		if p.processErr != nil {
			return p.processErr
		}
		return complete(workCtx, p.reply)
	})
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
	if len(s.replies) != 1 || s.replies[0] != processingReplyText || len(s.editedContents) != 1 || s.editedContents[0] != processingErrorText || strings.Contains(s.editedContents[0], secret) {
		t.Fatalf("unsafe output was not replaced with safe error: replies=%q edits=%q", s.replies, s.editedContents)
	}
	if len(p.savedContent) != 0 {
		t.Fatalf("unsafe output persisted as assistant response: %q", p.savedContent)
	}
}

func TestHandleDoesNotSendProcessingReplyWhenQueueAdmissionFails(t *testing.T) {
	p := &processorFake{admissionErr: orchestrator.ErrBusy}
	s := &senderFake{}
	a, err := New(p, testQueue(t), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "busy", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: time.Now()}, "bot")
	if p.processed != 0 || len(p.saved) != 0 || len(s.replies) != 1 || s.replies[0] == processingReplyText {
		t.Fatalf("busy admission produced processing/final state: processed=%d saved=%v replies=%q", p.processed, p.saved, s.replies)
	}
}

func TestQueueFullDoesNotSendProcessingReply(t *testing.T) {
	queue, err := orchestrator.NewRequestQueue(orchestrator.QueueConfig{MaxQueued: 1, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Shutdown(context.Background()) })
	started := make(chan struct{})
	release := make(chan struct{})
	activeDone := make(chan error, 1)
	go func() {
		activeDone <- queue.Submit(context.Background(), "active", func(context.Context) error { close(started); <-release; return nil })
	}()
	<-started
	queuedDone := make(chan error, 1)
	queuedAccepted := make(chan struct{})
	go func() {
		queuedDone <- queue.SubmitWithAccepted(context.Background(), "queued", func(context.Context) error { close(queuedAccepted); return nil }, func(context.Context) error { return nil })
	}()
	<-queuedAccepted

	p := &processorFake{}
	s := &senderFake{}
	a, err := New(p, queue, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "queue-full", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: time.Now()}, "bot")
	if p.processed != 0 || len(s.replies) != 1 || s.replies[0] != processingErrorText || len(s.editedContents) != 0 {
		t.Fatalf("full queue handling: processed=%d replies=%q edits=%q", p.processed, s.replies, s.editedContents)
	}
	close(release)
	if err := <-activeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-queuedDone; err != nil {
		t.Fatal(err)
	}
}

func TestHandleDoesNotReplyToDuplicateEvent(t *testing.T) {
	p := &processorFake{admissionErr: storage.ErrDuplicateDiscordMessage}
	s := &senderFake{}
	a, err := New(p, testQueue(t), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "duplicate", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: time.Now()}, "bot")
	if p.processed != 0 || len(p.saved) != 0 || len(s.replies) != 0 {
		t.Fatalf("duplicate event was handled: processed=%d saved=%v replies=%q", p.processed, p.saved, s.replies)
	}
}

func TestProcessingReplySendFailurePreventsProcessing(t *testing.T) {
	p := &processorFake{}
	s := &senderFake{errors: []error{statusRetryError{status: 403}}}
	a, err := New(p, testQueue(t), s, Config{SendMaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "receipt-fails", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: time.Now()}, "bot")
	if p.processed != 0 || len(s.replies) != 0 || len(s.editedContents) != 0 || len(p.saved) != 0 {
		t.Fatalf("receipt failure did not stop request: processed=%d replies=%q edits=%q saved=%v", p.processed, s.replies, s.editedContents, p.saved)
	}
}

func TestProcessingErrorEditsReceiptAndDoesNotPersistAssistant(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "LLM", err: errors.New("private LLM failure")},
		{name: "MCP", err: errors.New("private Search MCP failure")},
		{name: "timeout", err: context.DeadlineExceeded},
	} {
		t.Run(failure.name, func(t *testing.T) {
			p := &processorFake{processErr: failure.err}
			s := &senderFake{}
			a, err := New(p, testQueue(t), s, Config{})
			if err != nil {
				t.Fatal(err)
			}
			a.Handle(context.Background(), Incoming{ID: "processing-fails", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: time.Now()}, "bot")
			if len(s.replies) != 1 || s.replies[0] != processingReplyText || len(s.editedContents) != 1 || s.editedContents[0] != processingErrorText || s.editedIDs[0] != "reply-id" || len(p.saved) != 0 {
				t.Fatalf("processing error state: replies=%q edits=%q ids=%q saved=%v", s.replies, s.editedContents, s.editedIDs, p.saved)
			}
			if strings.Contains(s.editedContents[0], failure.err.Error()) {
				t.Fatalf("internal error leaked to Discord: %q", s.editedContents[0])
			}
		})
	}
}

func TestFinalEditFailureDoesNotPersistAssistant(t *testing.T) {
	p := &processorFake{}
	s := &senderFake{editErrors: []error{statusRetryError{status: 403}, statusRetryError{status: 403}}}
	a, err := New(p, testQueue(t), s, Config{SendMaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "edit-fails", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: time.Now()}, "bot")
	if len(s.replies) != 1 || s.replies[0] != processingReplyText || s.editCalls != 2 || len(p.saved) != 0 {
		t.Fatalf("final edit failure: replies=%q editCalls=%d saved=%v", s.replies, s.editCalls, p.saved)
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
	replies        []string
	channels       []string
	replyTo        []string
	sentTimes      []time.Time
	errors         []error
	failAt         int
	calls          int
	createdAt      time.Time
	editedContents []string
	editedChannels []string
	editedIDs      []string
	editErrors     []error
	editCalls      int
	replyIDs       []string
}

func (s *senderFake) Reply(_ context.Context, replyTo, channel, content string) (SentMessage, error) {
	s.calls++
	s.replyTo = append(s.replyTo, replyTo)
	if len(s.errors) > 0 {
		err := s.errors[0]
		s.errors = s.errors[1:]
		if err != nil {
			return SentMessage{}, err
		}
	}
	if s.failAt == s.calls {
		return SentMessage{}, errors.New("send failed")
	}
	s.replies = append(s.replies, content)
	s.channels = append(s.channels, channel)
	createdAt := time.Now()
	if !s.createdAt.IsZero() {
		createdAt = s.createdAt
	}
	s.sentTimes = append(s.sentTimes, createdAt)
	id := "reply-id"
	if index := len(s.replies) - 1; index >= 0 && index < len(s.replyIDs) {
		id = s.replyIDs[index]
	}
	return SentMessage{ID: id, CreatedAt: createdAt}, nil
}

func (s *senderFake) Edit(_ context.Context, channel, messageID, content string) error {
	s.editCalls++
	if len(s.editErrors) > 0 {
		err := s.editErrors[0]
		s.editErrors = s.editErrors[1:]
		if err != nil {
			return err
		}
	}
	s.editedContents = append(s.editedContents, content)
	s.editedChannels = append(s.editedChannels, channel)
	s.editedIDs = append(s.editedIDs, messageID)
	return nil
}

type statusRetryError struct {
	status int
	delay  time.Duration
}

func (e statusRetryError) Error() string             { return fmt.Sprintf("HTTP %d", e.status) }
func (e statusRetryError) StatusCode() int           { return e.status }
func (e statusRetryError) RetryAfter() time.Duration { return e.delay }

type networkRetryError struct{}

func (networkRetryError) Error() string   { return "temporary network failure" }
func (networkRetryError) Timeout() bool   { return false }
func (networkRetryError) Temporary() bool { return true }

var _ net.Error = networkRetryError{}

func TestDiscordRetryHonors429AndUsesBoundedBackoff(t *testing.T) {
	ctx := context.Background()
	var waits []time.Duration
	sender := &senderFake{errors: []error{statusRetryError{status: 429, delay: 1500 * time.Millisecond}}}
	adapter, err := New(&processorFake{}, testQueue(t), sender, Config{Wait: func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.sendWithRetry(ctx, "original", "channel", "reply"); err != nil {
		t.Fatalf("send after 429 retry: %v", err)
	}
	if sender.calls != 2 || len(waits) != 1 || waits[0] != 1500*time.Millisecond {
		t.Fatalf("429 attempts=%d waits=%v; want 2 attempts and exact retry_after", sender.calls, waits)
	}

	waits = nil
	sender = &senderFake{errors: []error{networkRetryError{}}}
	adapter, err = New(&processorFake{}, testQueue(t), sender, Config{Wait: func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.sendWithRetry(ctx, "original", "channel", "reply"); err != nil {
		t.Fatalf("send after network retry: %v", err)
	}
	if sender.calls != 2 || len(waits) != 1 || waits[0] != time.Second {
		t.Fatalf("network attempts=%d waits=%v; want one retry after 1s", sender.calls, waits)
	}

	waits = nil
	sender = &senderFake{errors: []error{
		statusRetryError{status: 503},
		statusRetryError{status: 502},
		statusRetryError{status: 500},
	}}
	adapter, err = New(&processorFake{}, testQueue(t), sender, Config{Wait: func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.sendWithRetry(ctx, "original", "channel", "reply"); err != nil {
		t.Fatalf("send after 5xx retries: %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if sender.calls != 4 || len(waits) != len(want) {
		t.Fatalf("5xx attempts=%d waits=%v", sender.calls, waits)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("retry delay[%d]=%s, want %s", i, waits[i], want[i])
		}
	}
}

func TestDiscordRetryDoesNotRetryClientErrorsOr429WithoutRetryAfter(t *testing.T) {
	for _, failure := range []error{statusRetryError{status: 400}, statusRetryError{status: 401}, statusRetryError{status: 403}, statusRetryError{status: 404}, statusRetryError{status: 429}} {
		sender := &senderFake{errors: []error{failure}}
		adapter, err := New(&processorFake{}, testQueue(t), sender, Config{Wait: func(context.Context, time.Duration) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.sendWithRetry(context.Background(), "original", "channel", "reply"); err == nil {
			t.Fatalf("send error for %v = nil", failure)
		}
		if sender.calls != 1 {
			t.Fatalf("HTTP %v retried %d times", failure, sender.calls)
		}
	}
}

func TestDiscordRetryStopsAfterConfiguredMaximum(t *testing.T) {
	waits := make([]time.Duration, 0, 3)
	sender := &senderFake{errors: []error{
		statusRetryError{status: 503},
		statusRetryError{status: 503},
		statusRetryError{status: 503},
		statusRetryError{status: 503},
	}}
	adapter, err := New(&processorFake{}, testQueue(t), sender, Config{Wait: func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.sendWithRetry(context.Background(), "original", "channel", "reply"); err == nil {
		t.Fatal("send succeeded after all attempts failed")
	}
	if sender.calls != 4 {
		t.Fatalf("send attempts=%d, want initial attempt plus 3 retries", sender.calls)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits=%v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("retry delay[%d]=%s, want %s", i, waits[i], want[i])
		}
	}
}

func TestDiscordEditUsesTheSameRetryPolicy(t *testing.T) {
	waits := make([]time.Duration, 0, 2)
	sender := &senderFake{editErrors: []error{statusRetryError{status: 429, delay: 1500 * time.Millisecond}, networkRetryError{}}}
	adapter, err := New(&processorFake{}, testQueue(t), sender, Config{Wait: func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.editWithRetry(context.Background(), "channel", "message", "answer"); err != nil {
		t.Fatalf("edit after retries: %v", err)
	}
	want := []time.Duration{1500 * time.Millisecond, 2 * time.Second}
	if sender.editCalls != 3 || len(waits) != len(want) || len(sender.editedContents) != 1 || sender.editedIDs[0] != "message" {
		t.Fatalf("edit calls=%d waits=%v successful edits=%v", sender.editCalls, waits, sender.editedContents)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("edit retry delay[%d]=%s, want %s", i, waits[i], want[i])
		}
	}
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
	if len(s.replies) != 1 || s.replies[0] != processingReplyText || len(s.editedContents) != 1 || s.editedContents[0] != "answer" || len(p.saved) != 1 {
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
	if len(s.replies) != 1 || s.replies[0] != processingReplyText || s.channels[0] != "thread" || len(s.editedChannels) != 1 || s.editedChannels[0] != "thread" {
		t.Fatalf("replies=%v channels=%v edits=%v", s.replies, s.channels, s.editedChannels)
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
	fixedNow := time.Now().UTC()
	a, err := New(p, testQueue(t), &senderFake{}, Config{Clock: func() time.Time { return fixedNow }, RequestIDGenerator: func() string { return "request-fixed" }})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 9, 23, 21, 30, 0, 0, time.FixedZone("discord", 9*60*60))
	receivedAt := fixedNow
	a.Handle(context.Background(), Incoming{ID: "utc", ChannelID: "dm", UserID: "u", Content: "question", IsDM: true, CreatedAt: createdAt}, "bot")
	if p.request.RequestID != "request-fixed" {
		t.Fatalf("request_id = %q, want injected ID", p.request.RequestID)
	}
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
	if len(s.replies) != 2 || s.replies[0] != processingReplyText || s.replies[1] != processingReplyText || len(p.saved) != 2 {
		t.Fatalf("burst accepted %d messages and sent %d replies; want 2 each", len(p.saved), len(s.replies))
	}
}

func TestShortFinalAnswerEditsProcessingReplyAndPersistsItsIDAndTime(t *testing.T) {
	createdAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	answer := strings.Repeat("a", defaultReplyMaxChars)
	p := &processorFake{reply: orchestrator.Reply{RequestID: "short", Content: answer}}
	s := &senderFake{createdAt: createdAt, replyIDs: []string{"processing-id"}}
	a, err := New(p, testQueue(t), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "q-short", ChannelID: "ch", UserID: "u", Content: "question", IsDM: true, CreatedAt: createdAt}, "bot")
	if len(s.replies) != 1 || s.replies[0] != processingReplyText || len(s.editedContents) != 1 || s.editedContents[0] != answer || s.editedIDs[0] != "processing-id" {
		t.Fatalf("short reply sends=%q edits=%q editIDs=%q", s.replies, s.editedContents, s.editedIDs)
	}
	if len(p.saved) != 1 || p.saved[0].DiscordMessageID != "processing-id" || !p.saved[0].CreatedAt.Equal(createdAt) || p.savedContent[0] != answer {
		t.Fatalf("saved reply=%+v content=%q", p.saved, p.savedContent)
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
	s := &senderFake{replyIDs: []string{"processing-id", "chunk-2-id"}}
	a, _ := New(p, testQueue(t), s, Config{})
	a.Handle(context.Background(), Incoming{ID: "q", ChannelID: "ch", UserID: "u", Content: "ask", IsDM: true, CreatedAt: time.Now()}, "bot")
	if len(s.replies) != 2 || s.replies[0] != processingReplyText || len(s.replies[1]) > defaultReplyMaxChars || len(s.replyTo) != 2 || s.replyTo[0] != "q" || s.replyTo[1] != "q" || len(s.sentTimes) != 2 || len(s.editedContents) != 1 || len(s.editedContents[0]) > defaultReplyMaxChars || s.editedIDs[0] != "processing-id" || len(p.saved) != 1 || !p.saved[0].Success || p.saved[0].DiscordMessageID != "processing-id" || !p.saved[0].CreatedAt.Equal(s.sentTimes[0]) || len(p.savedContent) != 1 || p.savedContent[0] != p.reply.Content {
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
	if len(parts) != 3 || len([]rune(parts[0])) > 10 || !strings.Contains(parts[0], "界") || strings.Join(parts, "") != strings.Repeat("界", 10)+"\n"+strings.Repeat("x", 10) {
		t.Fatalf("parts=%q", parts)
	}
}

func TestSplitMessageClosesAndReopensMarkdownCodeFences(t *testing.T) {
	content := "Result:\n```go\n" + strings.Repeat("fmt.Println(\"hello\")\n", 80) + "```\nDone."
	parts := splitMessage(content, 100)
	if len(parts) < 3 {
		t.Fatalf("split produced %d chunks, want multiple fenced chunks", len(parts))
	}
	joined := strings.Join(parts, "\n")
	if !strings.Contains(joined, "fmt.Println(\"hello\")") || !strings.Contains(joined, "Done.") {
		t.Fatalf("split lost original content: %q", joined)
	}
	for i, part := range parts {
		if len([]rune(part)) > 100 {
			t.Errorf("chunk %d has %d runes, over 100", i, len([]rune(part)))
		}
		if strings.Count(part, "```")%2 != 0 {
			t.Errorf("chunk %d leaves a Markdown code fence open: %q", i, part)
		}
	}
}
