//go:build darwin && unifiedlog

package imapclient

import (
	"context"
	"errors"
	"net"
	"testing"

	imapv2client "github.com/emersion/go-imap/v2/imapclient"
)

// This opt-in test emits a successful fake IMAP operation and a controlled
// connection failure to macOS Unified Logging for a private Console check.
func TestUnifiedLogFakeIMAPOperations(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}

	previous := dialListClient
	defer func() { dialListClient = previous }()
	dialListClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	if _, err := ListMailboxes(context.Background(), Config{Username: "test-user", Password: "test-password"}); err != nil {
		t.Fatalf("successful fake-server operation failed: %v", err)
	}

	dialListClient = func(string, *imapv2client.Options) (*imapv2client.Client, error) {
		return nil, errors.New("synthetic connection failure")
	}
	if _, err := ListMailboxes(context.Background(), Config{Username: "test-user", Password: "test-password"}); err == nil {
		t.Fatal("controlled fake connection failure unexpectedly succeeded")
	}
}
