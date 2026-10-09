package discordbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

type databaseLogError struct{}

func (databaseLogError) Error() string { return "database is locked (SQLITE_BUSY)" }
func (databaseLogError) Code() int     { return 5 }

func TestRequestFailureLogsCauseAndKeepsGenericDiscordReply(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &processorFake{admissionErr: fmt.Errorf("orchestrator: persist user message: %w", databaseLogError{})}
	s := &senderFake{}
	a, err := New(p, testQueue(t), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), Incoming{ID: "failed-question", ChannelID: "dm", UserID: "user", IsDM: true, Content: "private question", CreatedAt: time.Now()}, "bot")
	var entry struct {
		Event  string `json:"event"`
		Code   string `json:"error_code"`
		Detail struct {
			Message string `json:"message"`
			Code    int    `json:"cause_code"`
		} `json:"error_detail"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Event != "request_failed" || entry.Code != "request_failed" || entry.Detail.Code != 5 || !strings.Contains(entry.Detail.Message, "persist user message") || !strings.Contains(entry.Detail.Message, "SQLITE_BUSY") {
		t.Fatalf("missing diagnostic fields: %s", logs.String())
	}
	if len(s.replies) != 1 || s.replies[0] != processingErrorText || strings.Contains(logs.String(), "private question") {
		t.Fatalf("unexpected reply or content logging: replies=%q logs=%s", s.replies, logs.String())
	}
}

func TestErrorDetailsRedactsSecretsAndDiscordResponseBodies(t *testing.T) {
	a := &Adapter{outputGuard: newOutputGuard([]string{"configured-secret", "configured-secret-long"})}
	rest := &discordgo.RESTError{Response: &http.Response{StatusCode: 403, Status: "403 Forbidden"}, ResponseBody: []byte(`{"message":"private response body"}`), Message: &discordgo.APIErrorMessage{Code: 50013}}
	for _, tc := range []struct {
		name   string
		err    error
		want   string
		absent []string
	}{
		{"redacted", errors.New("database failed configured-secret-long token=unknown-secret private question"), "database failed", []string{"configured-secret", "-long", "unknown-secret", "private question"}},
		{"discord", fmt.Errorf("send: %w", rest), "50013", []string{"private response body"}},
		{"deadline", fmt.Errorf("generate answer: %w", context.DeadlineExceeded), "deadline_exceeded", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&out, nil))
			logger.Error("failure", a.errorDetails(tc.err, "private question"))
			if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("details: %s", out.String())
			}
			for _, value := range tc.absent {
				if strings.Contains(out.String(), value) {
					t.Fatalf("sensitive value logged: %s", out.String())
				}
			}
		})
	}
}
