package imapclient

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"imap-mail-mcp/internal/observability"
)

func TestListMailboxesFakeServer(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatalf("create Archive: %v", err)
	}

	previous := dialListClient
	dialListClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialListClient = previous }()
	var eventOutput bytes.Buffer
	restoreLogger := observability.SetTestWriter(&eventOutput)
	defer restoreLogger()

	result, err := ListMailboxes(context.Background(), Config{Username: "test-user", Password: "test-password"})
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(result.Mailboxes) < 2 {
		t.Fatalf("mailboxes = %#v, want INBOX and Archive", result.Mailboxes)
	}
	if result.Mailboxes[0].Name != "Archive" || result.Mailboxes[1].Name != "INBOX" {
		t.Fatalf("mailboxes are not sorted: %#v", result.Mailboxes)
	}
	for _, expected := range []string{"operation=connect", "operation=login", "operation=list"} {
		if !strings.Contains(eventOutput.String(), expected) {
			t.Errorf("operation logs missing %q: %s", expected, eventOutput.String())
		}
	}
}

func TestIMAPConnectionFailureLogOmitsRawError(t *testing.T) {
	previous := dialListClient
	const secret = "DISTINCTIVE-SECRET-MAILBOX"
	dialListClient = func(string, *imapv2client.Options) (*imapv2client.Client, error) {
		return nil, errors.New(secret)
	}
	defer func() { dialListClient = previous }()
	var eventOutput bytes.Buffer
	restoreLogger := observability.SetTestWriter(&eventOutput)
	defer restoreLogger()

	_, err := ListMailboxes(context.Background(), Config{Username: "test-user", Password: "test-password"})
	if err == nil {
		t.Fatal("ListMailboxes unexpectedly succeeded")
	}
	logs := eventOutput.String()
	if !strings.Contains(logs, "operation=connect") || !strings.Contains(logs, "result=error code=operation_failed level=error") {
		t.Fatalf("failed connection log = %q", logs)
	}
	if strings.Contains(logs, secret) {
		t.Fatalf("failed connection log leaked raw error: %q", logs)
	}
}

func TestSearchMailSupportsExplicitMailbox(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatalf("create Archive: %v", err)
	}
	appendSearchMessageToMailbox(t, user, "Archive", "2026-03-02T10:00:00Z", nil, "Archived message", "archive@example.com", "archive body")

	previous := dialSearchClient
	dialSearchClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialSearchClient = previous }()

	config := Config{Username: "test-user", Password: "test-password"}
	defaultResult, err := SearchMail(context.Background(), config, SearchRequest{Text: []string{"Archived message"}})
	if err != nil {
		t.Fatalf("default SearchMail: %v", err)
	}
	if len(defaultResult.Messages) != 0 {
		t.Fatalf("default mailbox unexpectedly returned archive mail: %#v", defaultResult.Messages)
	}

	result, err := SearchMail(context.Background(), config, SearchRequest{Mailbox: "Archive", Text: []string{"Archived message"}})
	if err != nil {
		t.Fatalf("explicit SearchMail: %v", err)
	}
	if len(result.Messages) != 1 || result.Messages[0].Mailbox != "Archive" {
		t.Fatalf("explicit mailbox result = %#v", result.Messages)
	}
}

func appendSearchMessageToMailbox(t *testing.T, user *imapmemserver.User, mailbox, date string, flags []imap.Flag, subject, from, body string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, date)
	if err != nil {
		t.Fatalf("parse message date: %v", err)
	}
	raw := "From: " + from + "\r\n" +
		"To: recipient@example.com\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + parsed.Format("Mon, 02 Jan 2006 15:04:05 -0700") + "\r\n" +
		"Message-ID: <archive-message@example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + body
	_, err = user.Append(mailbox, searchTestLiteral{Reader: bytes.NewReader([]byte(raw)), size: int64(len(raw))}, &imap.AppendOptions{Flags: flags, Time: parsed})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
}
