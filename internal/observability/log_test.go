package observability

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMeasureRecordsSafeSuccessFailureAndDuration(t *testing.T) {
	var output bytes.Buffer
	restore := SetTestWriter(&output)
	defer restore()

	value, err := Measure(context.Background(), "search", func() (int, error) { return 7, nil })
	if err != nil || value != 7 {
		t.Fatalf("Measure success returned %d, %v", value, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	secret := "DISTINCTIVE-SECRET-mail-content"
	gotErr := errors.New("raw server error containing " + secret)
	_, err = Measure(canceled, "fetch", func() (struct{}, error) { return struct{}{}, gotErr })
	if !errors.Is(err, gotErr) {
		t.Fatalf("Measure changed the operation error: %v", err)
	}
	ItemFailures("read_messages", 3)

	logs := output.String()
	for _, expected := range []string{
		"category=imap operation=search elapsed_ms=",
		"result=success level=default",
		"category=imap operation=fetch elapsed_ms=",
		"result=error code=canceled level=error",
		"category=server operation=read_messages elapsed_ms=0 result=error code=item_failures count=3 level=error",
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("log output missing %q: %s", expected, logs)
		}
	}
	if strings.Contains(logs, secret) || strings.Contains(logs, "raw server error") {
		t.Fatalf("event leaked the raw error: %s", logs)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		for _, field := range strings.Fields(line) {
			if !strings.HasPrefix(field, "elapsed_ms=") {
				continue
			}
			elapsed, err := strconv.ParseInt(strings.TrimPrefix(field, "elapsed_ms="), 10, 64)
			if err != nil || elapsed < 0 {
				t.Fatalf("invalid elapsed duration in event %q", line)
			}
		}
	}
}

func TestPlatformLoggerNeverWritesToStdout(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout capture: %v", err)
	}
	previous := os.Stdout
	os.Stdout = writer
	writePlatformEvent(categoryIMAP, false, "category=imap operation=search elapsed_ms=1 result=success")
	_ = writer.Close()
	os.Stdout = previous
	defer reader.Close()
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	if len(contents) != 0 {
		t.Fatalf("logging wrote to stdout: %q", contents)
	}
}

func TestMeasureTimeoutUsesFixedCode(t *testing.T) {
	var output bytes.Buffer
	restore := SetTestWriter(&output)
	defer restore()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := Measure(ctx, "connect", func() (struct{}, error) { return struct{}{}, context.DeadlineExceeded })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Measure returned %v", err)
	}
	if !strings.Contains(output.String(), "category=imap operation=connect elapsed_ms=") || !strings.Contains(output.String(), "result=error code=timeout level=error") {
		t.Fatalf("timeout event = %q", output.String())
	}
}
