package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"imap-mail-mcp/internal/imapclient"
	"imap-mail-mcp/internal/observability"
)

func main() {
	if err := imapclient.LoadDotEnv(".env"); err != nil {
		logServerFailure("startup", err)
		fmt.Fprintln(os.Stderr, "imap-mail-mcp: unable to load environment")
		os.Exit(1)
	}
	if err := newServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil && !errors.Is(err, io.EOF) {
		logServerFailure("run", err)
		fmt.Fprintln(os.Stderr, "imap-mail-mcp: server stopped unexpectedly")
		os.Exit(1)
	}
}

func logServerFailure(operation string, err error) {
	observability.ServerFailure(operation, err)
}

func logToolItemFailures(operation string, count int) {
	observability.ItemFailures(operation, count)
}

func logExportFailures(result imapclient.ExportMessagesResult) {
	logToolItemFailures("export_messages", result.FailedMessages+result.FailedAttachments+result.FailedMailboxes)
}

func newServer() *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "imap-mail-mcp", Version: "0.1.0"},
		&mcp.ServerOptions{Instructions: "A local IMAP hello, connection check, mailbox listing, mailbox search, message reading, attachment download, explicit message move, bulk export, and draft creation tool. Apple iCloud is the default endpoint and INBOX is the default mailbox."},
	)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hello",
		Description: "Return a fixed greeting.",
	}, hello)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_imap_connection",
		Description: "Connect to the configured IMAP account and return its advertised capabilities.",
	}, checkIMAPConnection)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_mailboxes",
		Description: "List the configured IMAP account's available mailboxes and their server-reported attributes.",
	}, listMailboxes)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_mail",
		Description: "Search a read-only IMAP mailbox with typed filters and return compact message metadata; defaults to INBOX.",
	}, searchMail)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_messages",
		Description: "Read selected messages from one mailbox as bounded plain text with attachment metadata, without changing flags.",
	}, readMessages)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "download_attachments",
		Description: "Download explicitly selected attachments from one mailbox to an existing directory without changing flags or overwriting files.",
	}, downloadAttachments)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "move_messages",
		Description: "Move explicitly selected messages from one existing mailbox to another using UID references.",
	}, moveMessages)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "export_messages",
		Description: "Export matching messages from one or more mailboxes as complete .eml files, optionally including decoded attachments.",
	}, exportMessages)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_draft",
		Description: "Create a new plain-text draft or reply draft in the server-reported Drafts mailbox. This never sends mail.",
		InputSchema: json.RawMessage(createDraftInputSchema),
	}, createDraft)

	return server
}

type helloOutput struct {
	Message string `json:"message"`
}

func hello(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, helloOutput, error) {
	return nil, helloOutput{Message: "Hello, world!"}, nil
}

type connectionOutput struct {
	Connected    bool     `json:"connected"`
	Capabilities []string `json:"capabilities"`
}

func checkIMAPConnection(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, connectionOutput, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, connectionOutput{}, imapclient.ErrInvalidConfiguration
	}

	capabilities, err := imapclient.CheckConnection(ctx, config)
	if err != nil {
		return nil, connectionOutput{}, imapclient.ErrConnectionFailed
	}
	return nil, connectionOutput{Connected: true, Capabilities: capabilities}, nil
}

func listMailboxes(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, imapclient.ListMailboxesResult, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, imapclient.ListMailboxesResult{}, imapclient.ErrInvalidConfiguration
	}

	result, err := imapclient.ListMailboxes(ctx, config)
	if err != nil {
		if errors.Is(err, imapclient.ErrMailboxFailed) {
			return nil, imapclient.ListMailboxesResult{}, imapclient.ErrMailboxFailed
		}
		return nil, imapclient.ListMailboxesResult{}, err
	}
	return nil, result, nil
}

func searchMail(ctx context.Context, _ *mcp.CallToolRequest, request imapclient.SearchRequest) (*mcp.CallToolResult, imapclient.SearchResult, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, imapclient.SearchResult{}, imapclient.ErrInvalidConfiguration
	}

	result, err := imapclient.SearchMail(ctx, config, request)
	if err != nil {
		if errors.Is(err, imapclient.ErrInvalidSearch) {
			return nil, imapclient.SearchResult{}, imapclient.ErrInvalidSearch
		}
		return nil, imapclient.SearchResult{}, imapclient.ErrSearchFailed
	}
	return nil, result, nil
}

func readMessages(ctx context.Context, _ *mcp.CallToolRequest, request imapclient.ReadMessagesRequest) (*mcp.CallToolResult, imapclient.ReadMessagesResult, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, imapclient.ReadMessagesResult{}, imapclient.ErrInvalidConfiguration
	}

	result, err := imapclient.ReadMessages(ctx, config, request)
	if err != nil {
		if errors.Is(err, imapclient.ErrInvalidRead) {
			return nil, imapclient.ReadMessagesResult{}, imapclient.ErrInvalidRead
		}
		return nil, imapclient.ReadMessagesResult{}, imapclient.ErrReadFailed
	}
	failedItems := 0
	for _, message := range result.Messages {
		if message.Error != "" {
			failedItems++
		}
	}
	logToolItemFailures("read_messages", failedItems)
	return nil, result, nil
}

func downloadAttachments(ctx context.Context, _ *mcp.CallToolRequest, request imapclient.DownloadAttachmentsRequest) (*mcp.CallToolResult, imapclient.DownloadAttachmentsResult, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, imapclient.DownloadAttachmentsResult{}, imapclient.ErrInvalidConfiguration
	}

	result, err := imapclient.DownloadAttachments(ctx, config, request)
	if err != nil {
		if errors.Is(err, imapclient.ErrInvalidDownload) {
			return nil, imapclient.DownloadAttachmentsResult{}, imapclient.ErrInvalidDownload
		}
		return nil, imapclient.DownloadAttachmentsResult{}, imapclient.ErrDownloadFailed
	}
	failedItems := 0
	for _, attachment := range result.Attachments {
		if attachment.Error != "" {
			failedItems++
		}
	}
	logToolItemFailures("download_attachments", failedItems)
	return nil, result, nil
}

func moveMessages(ctx context.Context, _ *mcp.CallToolRequest, request imapclient.MoveMessagesRequest) (*mcp.CallToolResult, imapclient.MoveMessagesResult, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, imapclient.MoveMessagesResult{}, imapclient.ErrInvalidConfiguration
	}

	result, err := imapclient.MoveMessages(ctx, config, request)
	if err != nil {
		if errors.Is(err, imapclient.ErrInvalidMove) {
			return nil, imapclient.MoveMessagesResult{}, imapclient.ErrInvalidMove
		}
		return nil, imapclient.MoveMessagesResult{}, imapclient.ErrMoveFailed
	}
	failedItems := 0
	for _, message := range result.Messages {
		if message.Status != "moved" {
			failedItems++
		}
	}
	logToolItemFailures("move_messages", failedItems)
	return nil, result, nil
}

func exportMessages(ctx context.Context, _ *mcp.CallToolRequest, request imapclient.ExportMessagesRequest) (*mcp.CallToolResult, imapclient.ExportMessagesResult, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, imapclient.ExportMessagesResult{}, imapclient.ErrInvalidConfiguration
	}

	result, err := imapclient.ExportMessages(ctx, config, request)
	if err != nil {
		if errors.Is(err, imapclient.ErrInvalidExport) {
			return nil, imapclient.ExportMessagesResult{}, imapclient.ErrInvalidExport
		}
		return nil, imapclient.ExportMessagesResult{}, imapclient.ErrExportFailed
	}
	logExportFailures(result)
	return nil, result, nil
}

func createDraft(ctx context.Context, _ *mcp.CallToolRequest, request imapclient.CreateDraftRequest) (*mcp.CallToolResult, imapclient.CreateDraftResult, error) {
	config, err := imapclient.LoadConfig(os.Getenv)
	if err != nil {
		return nil, imapclient.CreateDraftResult{}, imapclient.ErrInvalidConfiguration
	}

	result, err := imapclient.CreateDraft(ctx, config, request)
	if err != nil {
		switch {
		case errors.Is(err, imapclient.ErrInvalidDraft):
			return nil, imapclient.CreateDraftResult{}, imapclient.ErrInvalidDraft
		case errors.Is(err, imapclient.ErrInvalidConfiguration):
			return nil, imapclient.CreateDraftResult{}, imapclient.ErrInvalidConfiguration
		default:
			return nil, imapclient.CreateDraftResult{}, imapclient.ErrDraftFailed
		}
	}
	return nil, result, nil
}

const createDraftInputSchema = `{
	"type":"object",
	"oneOf":[
		{
			"type":"object",
			"properties":{
				"mode":{"type":"string","const":"new"},
				"to":{"type":"array","minItems":1,"maxItems":20,"items":{"type":"string"}},
				"cc":{"type":"array","maxItems":20,"items":{"type":"string"}},
				"bcc":{"type":"array","maxItems":20,"items":{"type":"string"}},
				"subject":{"type":"string","minLength":1,"maxLength":500},
				"text":{"type":"string","minLength":1,"maxLength":131072}
			},
			"required":["mode","to","subject","text"],
			"additionalProperties":false
		},
		{
			"type":"object",
			"properties":{
				"mode":{"type":"string","const":"reply"},
				"reply_to":{
					"type":"object",
					"properties":{
						"mailbox":{"type":"string"},
						"uid_validity":{"type":"integer","minimum":1,"maximum":4294967295},
						"uid":{"type":"integer","minimum":1,"maximum":4294967295}
					},
					"required":["mailbox","uid_validity","uid"],
					"additionalProperties":false
				},
				"text":{"type":"string","minLength":1,"maxLength":131072}
			},
			"required":["mode","reply_to","text"],
			"additionalProperties":false
		}
	]
}`
