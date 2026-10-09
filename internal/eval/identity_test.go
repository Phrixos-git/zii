package eval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Phrixos-git/zii/internal/chat"
	"github.com/Phrixos-git/zii/internal/orchestrator"
	"github.com/Phrixos-git/zii/internal/searchmcp"
)

func TestEvaluationSendsSharedIdentityPromptAndHashesIt(t *testing.T) {
	for _, mode := range []string{"auto", "none"} {
		t.Run(mode, func(t *testing.T) {
			var sent string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []chat.Message `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				sent = req.Messages[0].Content
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(completion("stop", "Zii 1.2", "", nil, nil)))
			}))
			defer server.Close()
			p := profileFor(server.URL)
			report, err := Run(context.Background(), p, Suite{Version: 1, Cases: []Case{{ID: "identity", Category: "identity", Tools: mode, Prompt: "あなたは誰？"}}}, RunOptions{Runs: 1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := p.clientConfig()
			if err != nil {
				t.Fatal(err)
			}
			registry, err := searchmcp.NewRegistry(FixtureTools())
			if err != nil {
				t.Fatal(err)
			}
			reg := caseRegistry{Registry: registry, enabled: mode == "auto"}
			want := orchestrator.BuildSystemPrompt(cfg.Profile, reg.LLMTools())
			if sent != want {
				t.Fatal("evaluation diverges from production prompt assembly")
			}
			if report.Samples[0].SystemPromptHash != hash(sent) {
				t.Fatal("sample hash does not represent sent prompt")
			}
			if report.SystemPromptHash != hash(orchestrator.BuildSystemPrompt(cfg.Profile, reg.Registry.LLMTools())) {
				t.Fatal("report prompt hash excludes resolved runtime facts")
			}
		})
	}
}
