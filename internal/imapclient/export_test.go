package imapclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
)

func TestExportMessagesFakeServer(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatalf("create Archive: %v", err)
	}
	appendExportMessage(t, user, "INBOX", "first@example.com", "Inbox message", false)
	appendExportBrokenMessage(t, user, "INBOX")
	appendExportMessage(t, user, "Archive", "archive@example.com", "Archive message", true)

	previousExport := dialExportClient
	previousSearch := dialSearchClient
	fakeDial := func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	dialExportClient = fakeDial
	dialSearchClient = fakeDial
	defer func() {
		dialExportClient = previousExport
		dialSearchClient = previousSearch
	}()

	destination := t.TempDir()
	result, err := ExportMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, ExportMessagesRequest{
		Mailboxes:          []string{"INBOX", "Archive"},
		Destination:        destination,
		IncludeAttachments: true,
	})
	if err != nil {
		t.Fatalf("ExportMessages: %v", err)
	}
	if result.Status != "partial" || result.FailedMailboxes != 0 || result.MatchedMessages != 3 || result.ExportedMessages != 2 || result.ExportedAttachments != 1 || result.FailedMessages != 1 || result.FailedAttachments != 1 {
		t.Fatalf("export summary = %#v", result)
	}
	manifestData, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest exportManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Messages) != 3 {
		t.Fatalf("manifest messages = %d, want 3", len(manifest.Messages))
	}
	seenMailboxes := map[string]bool{}
	for _, message := range manifest.Messages {
		seenMailboxes[message.Reference.Mailbox] = true
		if message.EMLPath == "" {
			t.Fatalf("manifest message = %#v", message)
		}
		if _, err := os.Stat(message.EMLPath); err != nil {
			t.Fatalf("stat exported message: %v", err)
		}
		if message.Reference.Mailbox == "Archive" {
			if len(message.Attachments) != 1 || message.Attachments[0].Status != "exported" {
				t.Fatalf("archive attachments = %#v", message.Attachments)
			}
			attachmentData, err := os.ReadFile(message.Attachments[0].Path)
			if err != nil {
				t.Fatalf("read exported attachment: %v", err)
			}
			if string(attachmentData) != "archive attachment" {
				t.Fatalf("attachment data = %q", attachmentData)
			}
		}
	}
	if !seenMailboxes["INBOX"] || !seenMailboxes["Archive"] {
		t.Fatalf("manifest mailboxes = %#v", seenMailboxes)
	}

	flags, err := SearchMail(context.Background(), Config{Username: "test-user", Password: "test-password"}, SearchRequest{Mailbox: "INBOX", Limit: 10})
	if err != nil {
		t.Fatalf("search exported INBOX: %v", err)
	}
	if len(flags.Messages) != 2 || len(flags.Messages[0].Flags) != 0 || len(flags.Messages[1].Flags) != 0 {
		t.Fatalf("export changed flags: %#v", flags.Messages)
	}
}

func TestExportMessagesEmptyResult(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	previous := dialExportClient
	dialExportClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialExportClient = previous }()

	result, err := ExportMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, ExportMessagesRequest{Destination: t.TempDir()})
	if err != nil {
		t.Fatalf("ExportMessages empty: %v", err)
	}
	if result.MatchedMessages != 0 || result.ExportedMessages != 0 {
		t.Fatalf("empty export summary = %#v", result)
	}
	if result.Status != "complete" || result.FailedMailboxes != 0 {
		t.Fatalf("empty export status = %#v, want complete with no failed mailboxes", result)
	}
}

func TestExportMessagesAllMailboxesMissingReportsFailedAndManifest(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	previous := dialExportClient
	dialExportClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialExportClient = previous }()

	result, err := ExportMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, ExportMessagesRequest{
		Mailboxes:   []string{"Missing"},
		Destination: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ExportMessages: %v", err)
	}
	if result.Status != "failed" || result.FailedMailboxes != 1 || result.MatchedMessages != 0 || result.ManifestPath == "" {
		t.Fatalf("all-missing-mailbox summary = %#v", result)
	}
	manifest := readExportTestManifest(t, result.ManifestPath)
	if len(manifest.MailboxErrors) != 1 || manifest.MailboxErrors[0].Error != "mailbox not found" {
		t.Fatalf("mailbox errors = %#v", manifest.MailboxErrors)
	}
}

func TestExportMessagesMailboxWorkFailureIsPartial(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	for _, mailbox := range []string{"INBOX", "Archive"} {
		if err := user.Create(mailbox, nil); err != nil {
			t.Fatalf("create mailbox: %v", err)
		}
	}
	previousDial := dialExportClient
	dialExportClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialExportClient = previousDial }()
	previousWork := exportMailboxWork
	exportMailboxWork = func(ctx context.Context, client *imapv2client.Client, mailbox, runDirectory string, request ExportMessagesRequest, manifest *exportManifest, manifestPath string) error {
		if mailbox == "Archive" {
			return errors.New("simulated mailbox search failure")
		}
		return exportMailbox(ctx, client, mailbox, runDirectory, request, manifest, manifestPath)
	}
	defer func() { exportMailboxWork = previousWork }()

	result, err := ExportMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, ExportMessagesRequest{
		Mailboxes:   []string{"INBOX", "Archive"},
		Destination: t.TempDir(),
		UIDRanges:   []UIDRange{{Start: 99, End: 99}},
	})
	if err != nil {
		t.Fatalf("ExportMessages: %v", err)
	}
	if result.Status != "partial" || result.FailedMailboxes != 1 || result.MatchedMessages != 0 || result.ManifestPath == "" {
		t.Fatalf("mixed mailbox summary = %#v", result)
	}
	manifest := readExportTestManifest(t, result.ManifestPath)
	if len(manifest.MailboxErrors) != 1 || manifest.MailboxErrors[0].Mailbox != "Archive" || manifest.MailboxErrors[0].Error != "mailbox export failed" {
		t.Fatalf("mailbox errors = %#v", manifest.MailboxErrors)
	}
}

func readExportTestManifest(t *testing.T, path string) exportManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest exportManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return manifest
}

func TestExportMessagesUnionsUIDRanges(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	appendExportMessage(t, user, "INBOX", "first@example.com", "First message", false)
	appendExportMessage(t, user, "INBOX", "second@example.com", "Second message", false)
	appendExportMessage(t, user, "INBOX", "third@example.com", "Third message", false)
	appendExportMessage(t, user, "INBOX", "fourth@example.com", "Fourth message", false)

	previous := dialExportClient
	dialExportClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialExportClient = previous }()

	tests := []struct {
		name     string
		ranges   []UIDRange
		wantUIDs []uint32
	}{
		{name: "disjoint singleton ranges", ranges: []UIDRange{{Start: 1, End: 1}, {Start: 3, End: 3}}, wantUIDs: []uint32{1, 3}},
		{name: "nonadjacent spans", ranges: []UIDRange{{Start: 1, End: 2}, {Start: 4, End: 4}}, wantUIDs: []uint32{1, 2, 4}},
		{name: "no UID matches", ranges: []UIDRange{{Start: 99, End: 99}, {Start: 100, End: 100}}, wantUIDs: []uint32{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ExportMessages(context.Background(), Config{Username: "test-user", Password: "test-password"}, ExportMessagesRequest{
				Destination: t.TempDir(),
				UIDRanges:   test.ranges,
			})
			if err != nil {
				t.Fatalf("ExportMessages: %v", err)
			}
			if result.MatchedMessages != len(test.wantUIDs) || result.ExportedMessages != len(test.wantUIDs) {
				t.Fatalf("export summary = %#v, want %d matched and exported", result, len(test.wantUIDs))
			}
			manifestData, err := os.ReadFile(result.ManifestPath)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			var manifest exportManifest
			if err := json.Unmarshal(manifestData, &manifest); err != nil {
				t.Fatalf("decode manifest: %v", err)
			}
			if len(manifest.Messages) != len(test.wantUIDs) {
				t.Fatalf("manifest has %d messages, want %d", len(manifest.Messages), len(test.wantUIDs))
			}
			wantUIDs := make(map[uint32]bool, len(test.wantUIDs))
			for _, uid := range test.wantUIDs {
				wantUIDs[uid] = true
			}
			for _, message := range manifest.Messages {
				if message.Reference.Mailbox != "INBOX" || message.Reference.UIDValidity == 0 {
					t.Fatalf("invalid exported identity: %#v", message.Reference)
				}
				if message.Status != "exported" || !wantUIDs[message.Reference.UID] {
					t.Fatalf("unexpected or duplicate exported message: %#v", message)
				}
				delete(wantUIDs, message.Reference.UID)
			}
			if len(wantUIDs) != 0 {
				t.Fatalf("missing exported UIDs: %#v", wantUIDs)
			}
		})
	}
}

func TestValidateExportRequest(t *testing.T) {
	if err := validateExportRequest(ExportMessagesRequest{Destination: "relative"}); err != ErrInvalidExport {
		t.Fatalf("relative destination error = %v", err)
	}
	if _, err := normalizeExportMailboxes([]string{"INBOX", "INBOX"}); err != nil {
		t.Fatalf("duplicate mailbox normalization: %v", err)
	}
	if _, err := normalizeExportMailboxes([]string{"INBOX\n"}); err != ErrInvalidExport {
		t.Fatalf("invalid mailbox error = %v", err)
	}
}

func appendExportMessage(t *testing.T, user interface {
	Append(string, imap.LiteralReader, *imap.AppendOptions) (*imap.AppendData, error)
}, mailbox, from, subject string, withAttachment bool) {
	t.Helper()
	raw := "From: " + from + "\r\nTo: victor@example.com\r\nSubject: " + subject + "\r\n"
	if withAttachment {
		raw += "Content-Type: multipart/mixed; boundary=export-boundary\r\n\r\n" +
			"--export-boundary\r\nContent-Type: text/plain\r\n\r\nArchive body\r\n" +
			"--export-boundary\r\nContent-Type: text/plain; name=archive.txt\r\n" +
			"Content-Disposition: attachment; filename=archive.txt\r\n" +
			"Content-Transfer-Encoding: base64\r\n\r\n" +
			"YXJjaGl2ZSBhdHRhY2htZW50\r\n--export-boundary--\r\n"
	} else {
		raw += "Content-Type: text/plain\r\n\r\nInbox body\r\n"
	}
	data := []byte(raw)
	if _, err := user.Append(mailbox, searchTestLiteral{Reader: bytes.NewReader(data), size: int64(len(data))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append export message: %v", err)
	}
}

func appendExportBrokenMessage(t *testing.T, user interface {
	Append(string, imap.LiteralReader, *imap.AppendOptions) (*imap.AppendData, error)
}, mailbox string) {
	t.Helper()
	raw := "From: broken@example.com\r\nTo: victor@example.com\r\nSubject: Broken attachment\r\n" +
		"Content-Type: multipart/mixed; boundary=broken-boundary\r\n\r\n" +
		"--broken-boundary\r\nContent-Type: text/plain\r\n\r\nBroken body\r\n" +
		"--broken-boundary\r\nContent-Type: application/octet-stream; name=broken.bin\r\n" +
		"Content-Disposition: attachment; filename=broken.bin\r\n" +
		"Content-Transfer-Encoding: unsupported\r\n\r\nnot decoded\r\n--broken-boundary--\r\n"
	data := []byte(raw)
	if _, err := user.Append(mailbox, searchTestLiteral{Reader: bytes.NewReader(data), size: int64(len(data))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append broken export message: %v", err)
	}
}

func TestExportMessageFilenameContainsMailboxAndUID(t *testing.T) {
	name := exportMessageBase(MessageReference{Mailbox: "My Mail", UIDValidity: 12, UID: 34})
	if !strings.Contains(name, "My_Mail") || !strings.Contains(name, "uidvalidity-12-uid-34") {
		t.Fatalf("export filename = %q", name)
	}
	if filepath.IsAbs(name) {
		t.Fatalf("export filename unexpectedly absolute: %q", name)
	}
}
