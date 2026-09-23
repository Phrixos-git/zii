package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Phrixos-git/zii/internal/discordbot"
	"github.com/Phrixos-git/zii/internal/llm"
	"github.com/Phrixos-git/zii/internal/orchestrator"
	"github.com/Phrixos-git/zii/internal/searchmcp"
	"github.com/Phrixos-git/zii/internal/storage"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(os.Getenv("LOG_LEVEL"))})))
	if err := run(); err != nil {
		slog.Error("application stopped", "component", "zii", "event", "fatal", "error_code", "startup_or_shutdown_error")
		os.Exit(1)
	}
}

func run() error {
	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	if token == "" {
		return errors.New("DISCORD_BOT_TOKEN is required")
	}
	dbPath := strings.TrimSpace(os.Getenv("BOT_DB_PATH"))
	if dbPath == "" || !filepath.IsAbs(dbPath) {
		return errors.New("BOT_DB_PATH must be an absolute path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		return err
	}
	conversationTTL, err := envDuration("CONVERSATION_TTL", 168*time.Hour)
	if err != nil {
		_ = db.Close()
		return err
	}
	repository, err := storage.NewRepositoryWithTTL(db, conversationTTL)
	if err != nil {
		_ = db.Close()
		return err
	}
	llmClient, err := llm.NewClientFromEnv()
	if err != nil {
		_ = db.Close()
		return err
	}
	searchTimeout, err := envDuration("SEARCH_MCP_TIMEOUT", 30*time.Second)
	if err != nil {
		_ = db.Close()
		return err
	}
	searchClient, err := searchmcp.NewClient(ctx, searchmcp.Config{URL: os.Getenv("SEARCH_MCP_URL"), Timeout: searchTimeout})
	if err != nil {
		_ = db.Close()
		return err
	}
	defer searchClient.Close()
	registry, err := searchmcp.NewRegistry(searchClient.Tools())
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	toolMax, err := envInt("TOOL_MAX_CALLS", 3)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	localMax, err := envInt("SEARCH_LOCAL_MAX_CALLS", 2)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	webMax, err := envInt("SEARCH_WEB_MAX_CALLS", 1)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	fetchMax, err := envInt("FETCH_PAGE_MAX_CALLS", 2)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	llmConcurrency, err := envInt("LLM_MAX_CONCURRENCY", 2)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	mcpConcurrency, err := envInt("MCP_MAX_CONCURRENCY", 4)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	loop, err := orchestrator.NewToolLoopWithConfig(llmClient, searchClient, registry, orchestrator.ToolLoopConfig{MaxCalls: toolMax, SearchLocalMaxCalls: localMax, SearchWebMaxCalls: webMax, FetchPageMaxCalls: fetchMax, LLMMaxConcurrency: llmConcurrency, MCPMaxConcurrency: mcpConcurrency})
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	maxTurns, err := envInt("CONVERSATION_MAX_TURNS", 5)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	maxTokens, err := envInt("CONVERSATION_MAX_TOKENS", 8192)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	service, err := orchestrator.NewService(repository, llmClient, loop, orchestrator.ServiceConfig{MaxHistoryTurns: maxTurns, MaxHistoryTokens: maxTokens})
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	queueSize, err := envInt("REQUEST_QUEUE_SIZE", 10)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	queueWait, err := envDuration("QUEUE_WAIT_TIMEOUT", 180*time.Second)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	requestTimeout, err := envDuration("REQUEST_TIMEOUT", 300*time.Second)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	queue, err := orchestrator.NewRequestQueue(orchestrator.QueueConfig{MaxQueued: queueSize, MaxRunning: 10, QueueWait: queueWait, RequestTimeout: requestTimeout})
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	replyMax, err := envInt("DISCORD_REPLY_MAX_CHARS", 1900)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	sendRetries, err := envInt("DISCORD_SEND_MAX_RETRIES", 3)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	gateway, err := discordbot.NewGateway(token)
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	adapter, err := discordbot.New(service, queue, gateway.Sender(), discordbot.Config{ReplyMaxChars: replyMax, SendMaxRetries: sendRetries, BlockedOutputValues: blockedOutputValues(dbPath)})
	if err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	if err := gateway.Bind(adapter); err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	if err := gateway.Open(); err != nil {
		_ = searchClient.Close()
		_ = db.Close()
		return err
	}
	slog.Info("Discord Gateway connected", "component", "discord_bot", "event", "connected")
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-signalCtx.Done()
	adapter.StopAccepting()
	graceCtx, graceCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer graceCancel()
	_ = adapter.Wait(graceCtx)
	queueErr := queue.Shutdown(graceCtx)
	closeErr := gateway.Close()
	_ = searchClient.Close()
	_ = llmClient.Close()
	dbErr := db.Close()
	if queueErr != nil {
		return fmt.Errorf("shutdown request queue: %w", queueErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Discord Gateway: %w", closeErr)
	}
	if dbErr != nil {
		return fmt.Errorf("close SQLite: %w", dbErr)
	}
	return nil
}

func blockedOutputValues(dbPath string) []string {
	values := []string{dbPath, "http://127.0.0.1:8080", "http://127.0.0.1:8081/mcp"}
	for _, key := range []string{"LLM_BASE_URL", "SEARCH_MCP_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values = append(values, value)
		}
	}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		name := strings.ToUpper(key)
		if strings.Contains(name, "TOKEN") || strings.Contains(name, "API_KEY") || strings.Contains(name, "APIKEY") || strings.Contains(name, "PASSWORD") || strings.Contains(name, "PASSWD") || strings.Contains(name, "SECRET") || strings.Contains(name, "MASTER_KEY") || strings.Contains(name, "CREDENTIAL") || strings.Contains(name, "AUTH_KEY") {
			values = append(values, value)
		}
	}
	return values
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return d, nil
}
func envInt(name string, fallback int) (int, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}
func logLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
