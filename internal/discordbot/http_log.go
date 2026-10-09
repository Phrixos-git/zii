package discordbot

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type discordRequestIDKey struct{}
type discordHTTPAttemptKey struct{}

type discordHTTPAttempt struct {
	requestID  string
	deliveryID string
	operation  string
	count      atomic.Int64
}

// Each logical reply or edit shares its counter across adapter and DiscordGo retries.
func withDiscordHTTPAttempt(ctx context.Context, operation string) context.Context {
	requestID, _ := ctx.Value(discordRequestIDKey{}).(string)
	return context.WithValue(ctx, discordHTTPAttemptKey{}, &discordHTTPAttempt{
		requestID: requestID, deliveryID: uuid.NewString(), operation: operation,
	})
}

type discordHTTPTransport struct{ base http.RoundTripper }

func (t discordHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempt, _ := req.Context().Value(discordHTTPAttemptKey{}).(*discordHTTPAttempt)
	if attempt == nil {
		return t.base.RoundTrip(req)
	}
	count := attempt.count.Add(1)
	started := time.Now()
	resp, err := t.base.RoundTrip(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	slog.DebugContext(req.Context(), "Discord HTTP attempt completed",
		"component", "discord_bot", "event", "discord_http_attempt",
		"request_id", attempt.requestID, "delivery_id", attempt.deliveryID,
		"operation", attempt.operation, "method", req.Method, "send_count", count,
		"http_status", status, "transport_error", err != nil,
		"duration_ms", time.Since(started).Milliseconds())
	return resp, err
}
