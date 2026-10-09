package discordbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type discordRoundTripFunc func(*http.Request) (*http.Response, error)

func (f discordRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func captureDiscordHTTPLogs(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestDiscordHTTPLogsInternalAndAdapterRetries(t *testing.T) {
	logs := captureDiscordHTTPLogs(t, slog.LevelDebug)
	client, err := NewGateway("private-token")
	if err != nil {
		t.Fatal(err)
	}
	gateway := client.(*Gateway)
	statuses := []int{502, 503, 200, 200}
	calls := 0
	gateway.session.Client.Transport = discordHTTPTransport{base: discordRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bot private-token" {
			t.Fatal("authorization header was changed")
		}
		status := statuses[calls]
		calls++
		body := `{"id":"message","timestamp":"2026-10-04T23:42:57+09:00","content":"private-response"}`
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}
	adapter, err := New(&processorFake{}, testQueue(t), gateway.Sender(), Config{Wait: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), discordRequestIDKey{}, "question-request")
	sent, err := adapter.sendWithRetry(ctx, "private-reply-target", "private-channel", "private-question")
	if err != nil || sent.ID != "message" || sent.CreatedAt.IsZero() {
		t.Fatalf("send result=%+v err=%v", sent, err)
	}
	if err := adapter.editWithRetry(ctx, "private-channel", sent.ID, "private-answer"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "private-") {
		t.Fatalf("sensitive data logged: %s", logs.String())
	}
	decoder := json.NewDecoder(logs)
	var deliveryID string
	for i, status := range statuses {
		var entry struct {
			Level, Event, Operation, Method string
			RequestID                       string `json:"request_id"`
			DeliveryID                      string `json:"delivery_id"`
			SendCount                       int    `json:"send_count"`
			HTTPStatus                      int    `json:"http_status"`
			TransportError                  bool   `json:"transport_error"`
		}
		if err := decoder.Decode(&entry); err != nil {
			t.Fatal(err)
		}
		wantCount, wantOperation, wantMethod := i+1, "reply", "POST"
		if i == 3 {
			wantCount, wantOperation, wantMethod = 1, "edit", "PATCH"
		}
		if entry.Level != "DEBUG" || entry.Event != "discord_http_attempt" || entry.RequestID != "question-request" || entry.DeliveryID == "" || entry.SendCount != wantCount || entry.HTTPStatus != status || entry.TransportError || entry.Operation != wantOperation || entry.Method != wantMethod {
			t.Fatalf("attempt %d: %+v", i, entry)
		}
		if i == 0 {
			deliveryID = entry.DeliveryID
		}
		if (i < 3 && entry.DeliveryID != deliveryID) || (i == 3 && entry.DeliveryID == deliveryID) {
			t.Fatalf("incorrect delivery grouping: %+v", entry)
		}
	}
	if calls != 4 {
		t.Fatalf("HTTP calls=%d", calls)
	}
	if decoder.Decode(new(any)) != io.EOF {
		t.Fatal("unexpected extra logs")
	}
}

func TestDiscordHTTPLogsTransportFailureAndDebugFiltering(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		t.Run(level.String(), func(t *testing.T) {
			logs := captureDiscordHTTPLogs(t, level)
			failure := errors.New("private-error private-token")
			transport := discordHTTPTransport{base: discordRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, failure })}
			ctx := withDiscordHTTPAttempt(context.Background(), "reply")
			req, err := http.NewRequestWithContext(ctx, "POST", "https://discord.com/private-url", strings.NewReader("private-body"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := transport.RoundTrip(req)
			if resp != nil || err != failure {
				t.Fatalf("result=%v err=%v", resp, err)
			}
			if strings.Contains(logs.String(), "private-") {
				t.Fatalf("sensitive data logged: %s", logs.String())
			}
			if level == slog.LevelInfo {
				if logs.Len() != 0 {
					t.Fatalf("DEBUG log at INFO: %s", logs.String())
				}
				return
			}
			var entry struct {
				HTTPStatus     int  `json:"http_status"`
				SendCount      int  `json:"send_count"`
				TransportError bool `json:"transport_error"`
			}
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry.HTTPStatus != 0 || entry.SendCount != 1 || !entry.TransportError {
				t.Fatalf("entry=%+v", entry)
			}
		})
	}
}

func TestNewGatewayInstallsDiscordHTTPLogging(t *testing.T) {
	client, err := NewGateway("test-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*Gateway).session.Client.Transport.(discordHTTPTransport); !ok {
		t.Fatal("Discord HTTP logging transport is not installed")
	}
}
