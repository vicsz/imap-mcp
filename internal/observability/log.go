// Package observability emits privacy-safe operational events.
package observability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	categoryIMAP    = "imap"
	categoryServer  = "server"
	resultSuccess   = "success"
	resultError     = "error"
	codeCanceled    = "canceled"
	codeTimeout     = "timeout"
	codeOperation   = "operation_failed"
	codeItemFailure = "item_failures"
)

var outputState struct {
	sync.RWMutex
	testWriter io.Writer
}

// Measure records one IMAP operation's completion and returns the operation's
// original result unchanged. The error itself is never included in the event.
func Measure[T any](ctx context.Context, operation string, call func() (T, error)) (T, error) {
	started := time.Now()
	value, err := call()
	record(categoryIMAP, operation, started, ctx, err, 0)
	return value, err
}

// MeasureError is Measure for operations that return only an error.
func MeasureError(ctx context.Context, operation string, call func() error) error {
	_, err := Measure(ctx, operation, func() (struct{}, error) {
		return struct{}{}, call()
	})
	return err
}

// CompleteIMAP records a streamed IMAP command after its response is drained.
func CompleteIMAP(operation string, started time.Time, ctx context.Context, err error) {
	record(categoryIMAP, operation, started, ctx, err, 0)
}

// ServerFailure records a sanitized server-level failure such as startup or
// an unexpected MCP transport termination.
func ServerFailure(operation string, err error) {
	if err == nil {
		return
	}
	record(categoryServer, operation, time.Now(), context.Background(), err, 0)
}

// ItemFailures records one aggregate event for a tool result containing
// per-item failures. No references or item-specific values are accepted.
func ItemFailures(operation string, count int) {
	if count < 1 {
		return
	}
	emit(categoryServer, true, formatEvent(categoryServer, operation, 0, resultError, codeItemFailure, count))
}

// SetTestWriter redirects events for tests and returns a function that restores
// the previous writer. Passing nil restores the platform logger.
func SetTestWriter(writer io.Writer) func() {
	outputState.Lock()
	previous := outputState.testWriter
	outputState.testWriter = writer
	outputState.Unlock()
	return func() {
		outputState.Lock()
		outputState.testWriter = previous
		outputState.Unlock()
	}
}

func record(category, operation string, started time.Time, ctx context.Context, err error, count int) {
	duration := time.Since(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	result, code, failed := resultSuccess, "", false
	if err != nil {
		result, code, failed = resultError, codeOperation, true
		var contextErr error
		if ctx != nil {
			contextErr = ctx.Err()
		}
		if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			code = codeTimeout
		} else if errors.Is(contextErr, context.Canceled) || errors.Is(err, context.Canceled) {
			code = codeCanceled
		} else {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				code = codeTimeout
			}
		}
	}
	emit(category, failed, formatEvent(category, operation, duration, result, code, count))
}

func formatEvent(category, operation string, duration int64, result, code string, count int) string {
	var event strings.Builder
	fmt.Fprintf(&event, "category=%s operation=%s elapsed_ms=%d result=%s", category, operation, duration, result)
	if code != "" {
		fmt.Fprintf(&event, " code=%s", code)
	}
	if count > 0 {
		fmt.Fprintf(&event, " count=%d", count)
	}
	return event.String()
}

func emit(category string, failed bool, message string) {
	level := "default"
	if failed {
		level = "error"
	}
	message += " level=" + level
	outputState.RLock()
	writer := outputState.testWriter
	if writer != nil {
		_, _ = fmt.Fprintln(writer, message)
		outputState.RUnlock()
		return
	}
	outputState.RUnlock()
	writePlatformEvent(category, failed, message)
}
