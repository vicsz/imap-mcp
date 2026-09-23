package imapclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestValidateDownloadRequest(t *testing.T) {
	destination := t.TempDir()
	cases := []DownloadAttachmentsRequest{
		{Destination: destination},
		{Destination: "relative", Attachments: []AttachmentReference{{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, PartID: "2"}}},
		{Destination: destination, Attachments: []AttachmentReference{{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, PartID: "../2"}}},
		{Destination: destination, Attachments: []AttachmentReference{
			{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, PartID: "2"},
			{Message: MessageReference{Mailbox: "Archive", UIDValidity: 1, UID: 2}, PartID: "2"},
		}},
	}
	for _, request := range cases {
		if err := validateDownloadRequest(request); err != ErrInvalidDownload {
			t.Errorf("request %#v returned %v, want ErrInvalidDownload", request, err)
		}
	}
}

func TestDownloadAttachmentsFakeServer(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	appendDownloadMessage(t, user, "From: erick@example.com\r\nTo: victor@example.com\r\nSubject: Assignment\r\nContent-Type: multipart/mixed; boundary=download-boundary\r\n\r\n--download-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nVisible body\r\n--download-boundary\r\nContent-Type: text/plain; name=hello.txt\r\nContent-Disposition: attachment; filename=hello.txt\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8gYXR0YWNobWVudA==\r\n--download-boundary\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nquoted=20attachment\r\n--download-boundary--\r\n")
	appendDownloadMessage(t, user, "From: plain@example.com\r\nTo: victor@example.com\r\nSubject: No attachment\r\nContent-Type: text/plain\r\n\r\nOnly text")

	previousDownload := dialDownloadClient
	previousSearch := dialSearchClient
	fakeDial := func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	dialDownloadClient = fakeDial
	dialSearchClient = fakeDial
	defer func() {
		dialDownloadClient = previousDownload
		dialSearchClient = previousSearch
	}()

	destination := t.TempDir()
	config := Config{Username: "test-user", Password: "test-password"}
	request := DownloadAttachmentsRequest{
		Destination: destination,
		Attachments: []AttachmentReference{
			{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, PartID: "2"},
			{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, PartID: "3"},
			{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 99, UID: 1}, PartID: "2"},
			{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 2}, PartID: "1"},
			{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, PartID: "1"},
		},
	}
	result, err := DownloadAttachments(context.Background(), config, request)
	if err != nil {
		t.Fatalf("DownloadAttachments: %v", err)
	}
	if len(result.Attachments) != len(request.Attachments) {
		t.Fatalf("result count = %d, want %d", len(result.Attachments), len(request.Attachments))
	}
	if result.Attachments[0].Status != "downloaded" || result.Attachments[0].SizeBytes != int64(len("hello attachment")) {
		t.Fatalf("base64 result = %#v", result.Attachments[0])
	}
	if result.Attachments[1].Status != "downloaded" || result.Attachments[1].SizeBytes != int64(len("quoted attachment")) {
		t.Fatalf("quoted-printable result = %#v", result.Attachments[1])
	}
	if result.Attachments[1].SavedPath == "" || !strings.HasSuffix(result.Attachments[1].SavedPath, "attachment-1-3.bin") {
		t.Fatalf("fallback filename = %#v", result.Attachments[1])
	}
	if result.Attachments[2].Error != "stale message reference" || result.Attachments[3].Error != "part is not a downloadable attachment" || result.Attachments[4].Error != "part is not a downloadable attachment" {
		t.Fatalf("partial failures = %#v", result.Attachments)
	}
	for _, downloaded := range result.Attachments[:2] {
		data, err := os.ReadFile(downloaded.SavedPath)
		if err != nil {
			t.Fatalf("read downloaded file: %v", err)
		}
		hash := sha256.Sum256(data)
		if downloaded.SHA256 != hex.EncodeToString(hash[:]) {
			t.Fatalf("hash = %q, want %q", downloaded.SHA256, hex.EncodeToString(hash[:]))
		}
		info, err := os.Stat(downloaded.SavedPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("downloaded permissions = %v, want 0600", info.Mode().Perm())
		}
	}

	collision, err := DownloadAttachments(context.Background(), config, DownloadAttachmentsRequest{
		Destination: destination,
		Attachments: []AttachmentReference{{Message: MessageReference{Mailbox: "INBOX", UIDValidity: 1, UID: 1}, PartID: "2"}},
	})
	if err != nil {
		t.Fatalf("collision request: %v", err)
	}
	if collision.Attachments[0].Status != "failed" || collision.Attachments[0].Error != "destination file already exists" {
		t.Fatalf("collision result = %#v", collision.Attachments[0])
	}

	flags, err := SearchMail(context.Background(), config, SearchRequest{WithFlags: []string{"\\Seen"}})
	if err != nil {
		t.Fatalf("SearchMail flags: %v", err)
	}
	if len(flags.Messages) != 0 {
		t.Fatalf("download changed flags: %#v", flags.Messages)
	}
}

func TestDownloadAttachmentsSupportsExplicitMailbox(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatalf("create Archive: %v", err)
	}
	appendDownloadMessageToMailbox(t, user, "Archive", "From: archive@example.com\r\nTo: victor@example.com\r\nSubject: Archive attachment\r\nContent-Type: multipart/mixed; boundary=archive-boundary\r\n\r\n--archive-boundary\r\nContent-Type: text/plain\r\n\r\nArchived body\r\n--archive-boundary\r\nContent-Type: text/plain; name=archive.txt\r\nContent-Disposition: attachment; filename=archive.txt\r\n\r\narchive attachment\r\n--archive-boundary--\r\n")
	status, err := user.Status("Archive", &imap.StatusOptions{UIDValidity: true})
	if err != nil || status.UIDValidity == 0 {
		t.Fatalf("archive status: %v", err)
	}

	previous := dialDownloadClient
	dialDownloadClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialDownloadClient = previous }()

	destination := t.TempDir()
	result, err := DownloadAttachments(context.Background(), Config{Username: "test-user", Password: "test-password"}, DownloadAttachmentsRequest{
		Destination: destination,
		Attachments: []AttachmentReference{{Message: MessageReference{Mailbox: "Archive", UIDValidity: status.UIDValidity, UID: 1}, PartID: "2"}},
	})
	if err != nil {
		t.Fatalf("DownloadAttachments: %v", err)
	}
	if result.Attachments[0].Status != "downloaded" || !strings.HasSuffix(result.Attachments[0].SavedPath, "archive.txt") {
		t.Fatalf("archive attachment = %#v", result.Attachments[0])
	}
}

func TestDownloadAttachmentLimitsAndCleanup(t *testing.T) {
	var output bytes.Buffer
	err := copyAttachment(strings.NewReader("123456"), &output, 5)
	if !errors.Is(err, errAttachmentTooLarge) {
		t.Fatalf("copyAttachment error = %v, want size error", err)
	}
	if output.Len() != 0 {
		t.Fatalf("oversized write produced %d bytes", output.Len())
	}

	destination := t.TempDir()
	symlink := filepath.Join(destination, "link")
	if err := os.Symlink(t.TempDir(), symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateDestinationDirectory(symlink); err != ErrInvalidDownload {
		t.Fatalf("symlink destination returned %v, want ErrInvalidDownload", err)
	}

	unsafe := &imap.BodyStructureSinglePart{
		Type: "application", Subtype: "octet-stream",
		Extended: &imap.BodyStructureSinglePartExt{Disposition: &imap.BodyStructureDisposition{
			Value: "attachment", Params: map[string]string{"filename": "../escape.bin"},
		}},
	}
	if _, err := attachmentFilename(unsafe, 1, "2"); err == nil {
		t.Fatal("path traversal filename was accepted")
	}
}

func appendDownloadMessage(t *testing.T, user *imapmemserver.User, raw string) {
	appendDownloadMessageToMailbox(t, user, "INBOX", raw)
}

func appendDownloadMessageToMailbox(t *testing.T, user *imapmemserver.User, mailbox, raw string) {
	t.Helper()
	data := []byte(raw)
	if _, err := user.Append(mailbox, searchTestLiteral{Reader: bytes.NewReader(data), size: int64(len(data))}, &imap.AppendOptions{}); err != nil {
		t.Fatalf("append download message: %v", err)
	}
}
