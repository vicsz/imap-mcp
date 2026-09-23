package imapclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestValidateReadRequest(t *testing.T) {
	cases := []ReadMessagesRequest{
		{},
		{Messages: make([]MessageReference, 11)},
		{Messages: []MessageReference{{Mailbox: "", UIDValidity: 1, UID: 1}}},
		{Messages: []MessageReference{{Mailbox: "INBOX", UID: 0, UIDValidity: 1}}},
		{Messages: []MessageReference{{Mailbox: "INBOX", UID: 1}}},
		{Messages: []MessageReference{{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, {Mailbox: "Archive", UIDValidity: 1, UID: 2}}},
	}
	for _, request := range cases {
		if err := validateReadRequest(request); err != ErrInvalidRead {
			t.Errorf("request %#v returned %v, want ErrInvalidRead", request, err)
		}
	}
}

func TestDecodeTextBodyTransferEncodings(t *testing.T) {
	base64Body := base64.StdEncoding.EncodeToString([]byte("base64 text"))
	cases := []struct {
		name     string
		body     []byte
		encoding string
		charset  string
		want     string
	}{
		{name: "base64", body: []byte(base64Body), encoding: "base64", want: "base64 text"},
		{name: "quoted printable", body: []byte("caf=E9"), encoding: "quoted-printable", charset: "iso-8859-1", want: "caf\u00e9"},
		{name: "utf8", body: []byte("plain"), encoding: "8bit", charset: "utf-8", want: "plain"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeTextBody(test.body, test.encoding, test.charset)
			if err != nil {
				t.Fatalf("decodeTextBody: %v", err)
			}
			if got != test.want {
				t.Fatalf("decoded text = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadMessagesFakeServerMIMEAndReferences(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	appendReadMessage(t, user, "From: plain@example.com\r\nTo: victor@example.com\r\nSubject: Plain\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nHello=20plain=21")
	appendReadMessage(t, user, "From: alt@example.com\r\nTo: victor@example.com\r\nSubject: Alternative\r\nContent-Type: multipart/alternative; boundary=alt-boundary\r\n\r\n--alt-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPlain alternative\r\n--alt-boundary\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>HTML alternative</p>\r\n--alt-boundary--\r\n")
	appendReadMessage(t, user, "From: html@example.com\r\nTo: victor@example.com\r\nSubject: HTML\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body><h1>Hello</h1><p>HTML only &amp; readable.</p><script>ignore me</script></body></html>")
	appendReadMessage(t, user, "From: mixed@example.com\r\nTo: victor@example.com\r\nSubject: Mixed\r\nContent-Type: multipart/mixed; boundary=mixed-boundary\r\n\r\n--mixed-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nVisible text\r\n--mixed-boundary\r\nContent-Type: application/pdf; name=invoice.pdf\r\nContent-Disposition: attachment; filename=invoice.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--mixed-boundary\r\nContent-Type: image/png; name=pixel.png\r\nContent-Disposition: inline; filename=pixel.png\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw==\r\n--mixed-boundary--\r\n")
	appendReadMessage(t, user, "From: latin@example.com\r\nTo: victor@example.com\r\nSubject: Latin\r\nContent-Type: text/plain; charset=iso-8859-1\r\n\r\nCaf\351")
	appendReadMessage(t, user, "From: empty@example.com\r\nTo: victor@example.com\r\nSubject: No text\r\nContent-Type: image/png\r\n\r\niVBORw==")
	appendReadMessage(t, user, "From: huge@example.com\r\nTo: victor@example.com\r\nSubject: Huge\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n"+strings.Repeat("x", maxReadTextBytes+100))

	previousRead := dialReadClient
	previousSearch := dialSearchClient
	fakeDial := func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	dialReadClient = fakeDial
	dialSearchClient = fakeDial
	defer func() {
		dialReadClient = previousRead
		dialSearchClient = previousSearch
	}()

	config := Config{Username: "test-user", Password: "test-password"}
	result, err := ReadMessages(context.Background(), config, ReadMessagesRequest{Messages: []MessageReference{
		{Mailbox: "INBOX", UIDValidity: 1, UID: 4},
		{Mailbox: "INBOX", UIDValidity: 1, UID: 1},
		{Mailbox: "INBOX", UIDValidity: 999, UID: 2},
		{Mailbox: "INBOX", UIDValidity: 1, UID: 999},
		{Mailbox: "INBOX", UIDValidity: 1, UID: 2},
		{Mailbox: "INBOX", UIDValidity: 1, UID: 3},
		{Mailbox: "INBOX", UIDValidity: 1, UID: 5},
		{Mailbox: "INBOX", UIDValidity: 1, UID: 6},
	}})
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(result.Messages) != 8 {
		t.Fatalf("result count = %d, want 8", len(result.Messages))
	}
	if result.Messages[0].Text != "Visible text" || len(result.Messages[0].Attachments) != 2 {
		t.Fatalf("mixed message = %#v", result.Messages[0])
	}
	if result.Messages[0].Attachments[0].PartID != "2" || result.Messages[0].Attachments[0].Filename != "invoice.pdf" {
		t.Fatalf("attachment metadata = %#v", result.Messages[0].Attachments)
	}
	if result.Messages[0].Attachments[1].PartID != "3" || result.Messages[0].Attachments[1].Filename != "pixel.png" {
		t.Fatalf("inline attachment metadata = %#v", result.Messages[0].Attachments)
	}
	if result.Messages[1].Text != "Hello plain!" || result.Messages[1].Source != "plain" {
		t.Fatalf("plain message = %#v", result.Messages[1])
	}
	if result.Messages[2].Error != "stale message reference" || result.Messages[2].Text != "" {
		t.Fatalf("stale reference = %#v", result.Messages[2])
	}
	if result.Messages[3].Error != "message not found" || result.Messages[3].Text != "" {
		t.Fatalf("missing reference = %#v", result.Messages[3])
	}
	if result.Messages[4].Text != "Plain alternative" || result.Messages[4].Source != "plain" {
		t.Fatalf("alternative message = %#v", result.Messages[4])
	}
	if result.Messages[5].Text != "Hello HTML only & readable." || result.Messages[5].Source != "html" {
		t.Fatalf("HTML message = %#v", result.Messages[5])
	}
	if result.Messages[6].Text != "Caf\u00e9" || result.Messages[6].Source != "plain" {
		t.Fatalf("latin message = %#v", result.Messages[6])
	}
	if result.Messages[7].Error != "no readable text body" {
		t.Fatalf("no-text message = %#v", result.Messages[7])
	}

	// The oversized message is read separately so the assertions above remain
	// easy to associate with their input positions.
	large, err := ReadMessages(context.Background(), config, ReadMessagesRequest{Messages: []MessageReference{{Mailbox: "INBOX", UIDValidity: 1, UID: 7}}})
	if err != nil {
		t.Fatalf("ReadMessages large: %v", err)
	}
	if len(large.Messages[0].Text) != maxReadTextBytes || !large.Messages[0].Truncated {
		t.Fatalf("large message bound = len %d truncated %v", len(large.Messages[0].Text), large.Messages[0].Truncated)
	}

	flags, err := SearchMail(context.Background(), config, SearchRequest{WithFlags: []string{"\\Seen"}})
	if err != nil {
		t.Fatalf("SearchMail flags: %v", err)
	}
	if len(flags.Messages) != 0 {
		t.Fatalf("read changed flags: %#v", flags.Messages)
	}
}

func TestReadMessagesSupportsExplicitMailbox(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatalf("create Archive: %v", err)
	}
	appendReadMessageToMailbox(t, user, "Archive", "From: archive@example.com\r\nTo: victor@example.com\r\nSubject: Archive\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nArchived text")
	status, err := user.Status("Archive", &imap.StatusOptions{UIDValidity: true})
	if err != nil || status.UIDValidity == 0 {
		t.Fatalf("archive status: %v", err)
	}

	previous := dialReadClient
	dialReadClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialReadClient = previous }()

	result, err := ReadMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, ReadMessagesRequest{Messages: []MessageReference{{Mailbox: "Archive", UIDValidity: status.UIDValidity, UID: 1}}})
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if result.Messages[0].Text != "Archived text" || result.Messages[0].Reference.Mailbox != "Archive" {
		t.Fatalf("archive message = %#v", result.Messages[0])
	}
}

func appendReadMessage(t *testing.T, user *imapmemserver.User, raw string) {
	appendReadMessageToMailbox(t, user, "INBOX", raw)
}

func appendReadMessageToMailbox(t *testing.T, user *imapmemserver.User, mailbox, raw string) {
	t.Helper()
	data := []byte(raw)
	if _, err := user.Append(mailbox, searchTestLiteral{Reader: bytes.NewReader(data), size: int64(len(data))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append read message: %v", err)
	}
}
