package orchestrator

import (
	"context"

	"github.com/Phrixos-git/zii/internal/llm"
)

type requestIDContextKey struct{}

func withRequestID(ctx context.Context, requestID string) context.Context {
	ctx = context.WithValue(ctx, requestIDContextKey{}, requestID)
	return llm.WithRequestID(ctx, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return value
}
