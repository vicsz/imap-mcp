package imapclient

import (
	"bytes"
	"context"
	"net"
	"testing"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
)

func TestValidateMoveRequest(t *testing.T) {
	cases := []MoveMessagesRequest{
		{},
		{Destination: "Archive"},
		{Destination: "", Messages: []MessageReference{{Mailbox: "INBOX", UIDValidity: 1, UID: 1}}},
		{Destination: "INBOX", Messages: []MessageReference{{Mailbox: "INBOX", UIDValidity: 1, UID: 1}}},
		{Destination: "Archive", Messages: []MessageReference{{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, {Mailbox: "Junk", UIDValidity: 1, UID: 2}}},
		{Destination: "Archive", Messages: []MessageReference{{Mailbox: "INBOX", UIDValidity: 0, UID: 1}}},
		{Destination: "Archive", Messages: []MessageReference{{Mailbox: "INBOX", UIDValidity: 1, UID: 0}}},
		{Destination: "Archive", Messages: []MessageReference{{Mailbox: "INBOX\n", UIDValidity: 1, UID: 1}}},
	}
	for _, request := range cases {
		if err := validateMoveRequest(request); err != ErrInvalidMove {
			t.Errorf("request %#v returned %v, want ErrInvalidMove", request, err)
		}
	}
}

func TestMoveMessagesFakeServer(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatalf("create Archive: %v", err)
	}
	appendMoveMessage(t, user, "INBOX", "first@example.com", "First message")
	appendMoveMessage(t, user, "INBOX", "second@example.com", "Second message")
	status, err := user.Status("INBOX", &imap.StatusOptions{UIDValidity: true})
	if err != nil || status.UIDValidity == 0 {
		t.Fatalf("INBOX status: %v", err)
	}

	previous := dialMoveClient
	dialMoveClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialMoveClient = previous }()

	result, err := MoveMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, MoveMessagesRequest{
		Destination: "Archive",
		Messages: []MessageReference{
			{Mailbox: "INBOX", UIDValidity: status.UIDValidity, UID: 1},
			{Mailbox: "INBOX", UIDValidity: status.UIDValidity + 1, UID: 2},
		},
	})
	if err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	if result.Messages[0].Status != "moved" || result.Messages[0].Error != "" {
		t.Fatalf("moved result = %#v", result.Messages[0])
	}
	if result.Messages[1].Status != "failed" || result.Messages[1].Error != "stale message reference" {
		t.Fatalf("stale result = %#v", result.Messages[1])
	}

	inboxStatus, err := user.Status("INBOX", &imap.StatusOptions{NumMessages: true})
	if err != nil {
		t.Fatalf("INBOX status after move: %v", err)
	}
	archiveStatus, err := user.Status("Archive", &imap.StatusOptions{NumMessages: true})
	if err != nil {
		t.Fatalf("Archive status after move: %v", err)
	}
	if inboxStatus.NumMessages == nil || archiveStatus.NumMessages == nil || *inboxStatus.NumMessages != 1 || *archiveStatus.NumMessages != 1 {
		t.Fatalf("message counts after move: INBOX=%v Archive=%v", inboxStatus.NumMessages, archiveStatus.NumMessages)
	}
}

func TestMoveMessagesRejectsUnknownDestination(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatalf("create Archive: %v", err)
	}

	previous := dialMoveClient
	dialMoveClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialMoveClient = previous }()

	_, err := MoveMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, MoveMessagesRequest{
		Destination: "Does Not Exist",
		Messages:    []MessageReference{{Mailbox: "INBOX", UIDValidity: 1, UID: 1}},
	})
	if err != ErrInvalidMove {
		t.Fatalf("MoveMessages error = %v, want ErrInvalidMove", err)
	}
}

func appendMoveMessage(t *testing.T, user interface {
	Append(string, imap.LiteralReader, *imap.AppendOptions) (*imap.AppendData, error)
}, mailbox, from, subject string) {
	t.Helper()
	raw := "From: " + from + "\r\nTo: victor@example.com\r\nSubject: " + subject + "\r\nContent-Type: text/plain\r\n\r\n" + subject
	data := []byte(raw)
	if _, err := user.Append(mailbox, searchTestLiteral{Reader: bytes.NewReader(data), size: int64(len(data))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append move message: %v", err)
	}
}
