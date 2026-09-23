package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"imap-mail-mcp/internal/imapclient"
	"imap-mail-mcp/internal/observability"
)

func TestHello(t *testing.T) {
	_, output, err := hello(context.Background(), nil, struct{}{})
	if err != nil {
		t.Fatalf("hello returned error: %v", err)
	}
	if output.Message != "Hello, world!" {
		t.Fatalf("hello message = %q", output.Message)
	}
}

func TestStartupAndPerItemFailuresAreLoggedSafely(t *testing.T) {
	var output bytes.Buffer
	restore := observability.SetTestWriter(&output)
	defer restore()

	secret := "DISTINCTIVE-SECRET-RECIPIENT"
	logServerFailure("startup", errors.New(secret))
	logToolItemFailures("read_messages", 2)
	logExportFailures(imapclient.ExportMessagesResult{FailedMailboxes: 1, FailedMessages: 2, FailedAttachments: 3})
	logs := output.String()
	for _, expected := range []string{
		"category=server operation=startup elapsed_ms=0 result=error code=operation_failed level=error",
		"category=server operation=read_messages elapsed_ms=0 result=error code=item_failures count=2 level=error",
		"category=server operation=export_messages elapsed_ms=0 result=error code=item_failures count=6 level=error",
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("log output missing %q: %s", expected, logs)
		}
	}
	if strings.Contains(logs, secret) {
		t.Fatalf("log output leaked a sensitive value: %s", logs)
	}
}

func TestServerRegistersExpectedTools(t *testing.T) {
	ctx := context.Background()
	server := newServer()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close()

	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools.Tools) != 9 {
		t.Fatalf("tool count = %d, want 9", len(tools.Tools))
	}
	toolNames := make(map[string]bool, len(tools.Tools))
	var draftSchema any
	for _, tool := range tools.Tools {
		toolNames[tool.Name] = true
		if tool.Name == "create_draft" {
			draftSchema = tool.InputSchema
		}
	}
	for _, name := range []string{"hello", "check_imap_connection", "list_mailboxes", "search_mail", "read_messages", "download_attachments", "move_messages", "export_messages", "create_draft"} {
		if !toolNames[name] {
			t.Fatalf("missing registered tool %q", name)
		}
	}
	schemaJSON, err := json.Marshal(draftSchema)
	if err != nil {
		t.Fatalf("marshal create_draft schema: %v", err)
	}
	var schema struct {
		OneOf []json.RawMessage `json:"oneOf"`
	}
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatalf("decode create_draft schema: %v", err)
	}
	if len(schema.OneOf) != 2 {
		t.Fatalf("create_draft schema has %d branches, want new and reply", len(schema.OneOf))
	}
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatal("hello returned an MCP error")
	}
	if result.StructuredContent == nil {
		t.Fatal("hello returned no structured content")
	}
}
