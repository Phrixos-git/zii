package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIReportsAndExitCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"42"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	}))
	defer server.Close()
	root := t.TempDir()
	profile := filepath.Join(root, "profile.yaml")
	cases := filepath.Join(root, "cases.yaml")
	if e := os.WriteFile(profile, []byte("version: 1\nname: simulated-cli\nmodel: scripted\nbase_url: "+server.URL+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(cases, []byte("version: 1\ncases:\n- id: answer\n  category: basic\n  prompt: '6 * 7'\n  tools: none\n  expect: {equals: '42'}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	var out, errOut bytes.Buffer
	call := func(args ...string) int {
		out.Reset()
		errOut.Reset()
		return execute(context.Background(), args, &out, &errOut)
	}
	if code := call("check", "--profile", profile, "--cases", cases); code != 0 {
		t.Fatal(code, errOut.String())
	}
	for _, name := range []string{"before", "after"} {
		dir := filepath.Join(root, name)
		if code := call("run", "--profile", profile, "--cases", cases, "--runs", "2", "--warmup", "1", "--out", dir, "--label", name); code != 0 {
			t.Fatal(code, errOut.String())
		}
		for _, file := range []string{"report.json", "results.csv", "summary.md", "samples.jsonl"} {
			if _, e := os.Stat(filepath.Join(dir, file)); e != nil {
				t.Fatal(e)
			}
		}
		data, e := os.ReadFile(filepath.Join(dir, "report.json"))
		if e != nil {
			t.Fatal(e)
		}
		var r struct {
			Complete bool                                 `json:"complete"`
			Summary  struct{ Executed, Pass, Warmup int } `json:"summary"`
		}
		if e = json.Unmarshal(data, &r); e != nil {
			t.Fatal(e)
		}
		if !r.Complete || r.Summary.Pass != 2 || r.Summary.Warmup != 1 {
			t.Fatalf("bad report %s", data)
		}
	}
	if code := call("compare", "--before", filepath.Join(root, "before", "report.json"), "--after", filepath.Join(root, "after", "report.json"), "--out", filepath.Join(root, "comparison")); code != 0 {
		t.Fatal(code, errOut.String())
	}
	if code := call("run", "--profile", profile, "--cases", cases, "--out", filepath.Join(root, "before")); code != 2 {
		t.Fatal("overwrite accepted", code)
	}
	if code := call("run", "--profile", profile, "--cases", cases, "--runs", "0"); code != 2 {
		t.Fatal("invalid run count", code)
	}
	if e := os.WriteFile(cases, []byte("version: 1\ncases:\n- id: mismatch\n  category: basic\n  prompt: '6 * 7'\n  tools: none\n  expect: {equals: 'wrong'}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if code := call("run", "--profile", profile, "--cases", cases, "--out", filepath.Join(root, "failure")); code != 1 {
		t.Fatal("failure exit code", code, errOut.String())
	}
}
