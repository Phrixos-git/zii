package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

func (c *Client) postJSON(ctx context.Context, path string, payload []byte) ([]byte, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("llm: client is nil")
	}
	if ctx == nil {
		return nil, errors.New("llm: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/" + strings.TrimLeft(path, "/")

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("llm: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt == 0 && ctx.Err() == nil && isRetryableConnectionError(err) {
				if waitErr := waitRetry(ctx, c.retryDelay); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, fmt.Errorf("llm: request failed: %w", err)
		}

		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			if attempt == 0 && ctx.Err() == nil && isRetryableConnectionError(readErr) {
				if waitErr := waitRetry(ctx, c.retryDelay); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, fmt.Errorf("llm: read response: %w", readErr)
		}
		if resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode <= 599 {
			if attempt == 0 {
				if waitErr := waitRetry(ctx, c.retryDelay); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, fmt.Errorf("llm: endpoint returned HTTP %d", resp.StatusCode)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("llm: endpoint returned HTTP %d", resp.StatusCode)
		}
		return body, nil
	}
	return nil, errors.New("llm: request failed after retry")
}

func isRetryableConnectionError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return !opErr.Timeout()
	}
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EPIPE)
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
