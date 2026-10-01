package llm

import "context"

type requestIDContextKey struct{}

type toolLoopDiagnosticsKey struct{}

// ToolLoopDiagnostics describes orchestration state without message bodies.
type ToolLoopDiagnostics struct {
	InferenceTurn       int
	ToolsDisabled       bool
	ToolsDisabledReason string
	RegisteredToolNames []string
}

func WithToolLoopDiagnostics(ctx context.Context, diagnostics ToolLoopDiagnostics) context.Context {
	diagnostics.RegisteredToolNames = append([]string(nil), diagnostics.RegisteredToolNames...)
	return context.WithValue(ctx, toolLoopDiagnosticsKey{}, diagnostics)
}

func toolLoopDiagnosticsFromContext(ctx context.Context) (ToolLoopDiagnostics, bool) {
	diagnostics, ok := ctx.Value(toolLoopDiagnosticsKey{}).(ToolLoopDiagnostics)
	return diagnostics, ok
}

// WithRequestID associates a request ID with LLM response metadata logs.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
