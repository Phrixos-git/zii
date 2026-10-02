package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Phrixos-git/zii/internal/eval"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(execute(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func execute(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errOut, "usage: zii-eval check|run|compare [flags]")
		return 2
	}
	if args[0] == "compare" {
		return compare(args[1:], out, errOut)
	}
	if args[0] != "check" && args[0] != "run" {
		fmt.Fprintln(errOut, "expected check, run or compare")
		return 2
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(errOut)
	profile := f.String("profile", "eval/profiles/example.yaml", "profile YAML")
	cases := f.String("cases", "eval/testcases", "case YAML or directory")
	dir := f.String("out", "", "new output directory")
	label := f.String("label", "", "label for this run")
	runs := f.Int("runs", 1, "measured suite repetitions")
	warmup := f.Int("warmup", 0, "full-suite warmup repetitions (excluded from metrics)")
	id := f.String("id", "", "select one case")
	category := f.String("category", "", "select category")
	save := f.Bool("save-answers", false, "save final content (reasoning_content is never saved)")
	strict := f.Bool("fail-on-warn", true, "exit 1 for recovered anomalies too")
	if e := f.Parse(args[1:]); e != nil {
		if e == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(errOut, "unexpected positional arguments")
		return 2
	}
	if *runs < 1 || *warmup < 0 {
		fmt.Fprintln(errOut, "runs must be positive and warmup nonnegative")
		return 2
	}
	p, e := eval.LoadProfile(*profile)
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	s, e := eval.LoadSuite(*cases)
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	if args[0] == "check" {
		fmt.Fprintf(out, "Valid profile %s; %d cases. No network requests made.\n", p.Name, len(s.Cases))
		return 0
	}
	if *label == "" {
		*label = p.Name
	}
	if *dir == "" {
		*dir = filepath.Join("reports", time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	journal, e := eval.CreateOutput(*dir)
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	defer journal.Close()
	encoder := json.NewEncoder(journal)
	// Runtime structured logs deliberately do not contain bodies. Keep the CLI
	// concise; complete state/attempt information is in the sanitized trace.
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(previous)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var journalErr error
	r, runErr := eval.Run(runCtx, p, s, eval.RunOptions{Runs: *runs, Warmup: *warmup, ID: *id, Category: *category, SaveAnswers: *save, Label: *label}, func(sample eval.Sample) {
		if journalErr == nil {
			journalErr = encoder.Encode(sample)
			if journalErr == nil {
				journalErr = journal.Sync()
			}
			if journalErr != nil {
				cancel()
			}
		}
		phase := "run"
		if sample.Warmup {
			phase = "warmup"
		}
		fmt.Fprintf(out, "%s %d %-28s %-4s %.0f ms\n", phase, sample.Repetition, sample.CaseID, sample.Status, sample.Metrics.TotalMS)
	})
	if e := eval.WriteReport(*dir, r); e != nil {
		fmt.Fprintln(errOut, "save report:", e)
		return 2
	}
	fmt.Fprintf(out, "PASS %d / WARN %d / FAIL %d / SKIP %d; %s\n", r.Summary.Pass, r.Summary.Warn, r.Summary.Fail, r.Summary.Skip, filepath.Join(*dir, "summary.md"))
	if journalErr != nil {
		fmt.Fprintln(errOut, "sample journal:", journalErr)
		return 2
	}
	if runErr != nil {
		fmt.Fprintln(errOut, runErr)
		if ctx.Err() != nil {
			return 130
		}
		return 2
	}
	if r.Summary.Fail > 0 || (*strict && r.Summary.Warn > 0) {
		return 1
	}
	return 0
}

func compare(args []string, out, errOut io.Writer) int {
	f := flag.NewFlagSet("compare", flag.ContinueOnError)
	f.SetOutput(errOut)
	before := f.String("before", "", "before report.json")
	after := f.String("after", "", "after report.json")
	dir := f.String("out", "", "new comparison directory")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || *before == "" || *after == "" {
		fmt.Fprintln(errOut, "--before and --after are required")
		return 2
	}
	a, e := eval.ReadReport(*before)
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	b, e := eval.ReadReport(*after)
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	c, e := eval.Compare(a, b)
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	if *dir == "" {
		*dir = filepath.Join("reports", "compare-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	journal, e := eval.CreateOutput(*dir)
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	_ = journal.Close()
	if e = eval.WriteComparison(*dir, c); e != nil {
		fmt.Fprintln(errOut, e)
		return 2
	}
	fmt.Fprintf(out, "Regressions %d / improvements %d; %s\n", c.Regressions, c.Improvements, filepath.Join(*dir, "comparison.md"))
	if c.Regressions > 0 {
		return 1
	}
	return 0
}
