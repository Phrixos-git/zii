package orchestrator

import (
	"context"

	"github.com/Phrixos-git/zii/internal/chat"
)

func (l *ToolLoop) prepareToolMarkupRetry(ctx context.Context, messages []chat.Message) ([]chat.Message, error) {
	compacted, err := compactToolResults(ctx, l.budget.counter, messages)
	if err != nil {
		return nil, err
	}
	const instruction = "The previous final response contained tool-call syntax. Give a concise natural-language answer in the user's language using only evidence already collected. Do not restart the investigation, search again, or call tools. Never output tool-call syntax as text."
	if len(compacted) > 0 && compacted[0].Role == "system" {
		updated := append([]chat.Message(nil), compacted...)
		updated[0].Content = instruction + "\n\n" + updated[0].Content
		return updated, nil
	}
	return append([]chat.Message{{Role: "system", Content: instruction}}, compacted...), nil
}
