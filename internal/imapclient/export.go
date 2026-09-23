package imapclient

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"imap-mail-mcp/internal/observability"
)

var (
	ErrInvalidExport  = errors.New("export request is invalid")
	ErrExportFailed   = errors.New("IMAP export failed")
	dialExportClient  = imapv2client.DialTLS
	exportMailboxWork = exportMailbox
)

type ExportMessagesRequest struct {
	Mailboxes          []string       `json:"mailboxes,omitempty"`
	Destination        string         `json:"destination"`
	IncludeAttachments bool           `json:"include_attachments,omitempty"`
	Since              string         `json:"since,omitempty"`
	Before             string         `json:"before,omitempty"`
	SentSince          string         `json:"sent_since,omitempty"`
	SentBefore         string         `json:"sent_before,omitempty"`
	Headers            []HeaderFilter `json:"headers,omitempty"`
	Body               []string       `json:"body,omitempty"`
	Text               []string       `json:"text,omitempty"`
	WithFlags          []string       `json:"with_flags,omitempty"`
	WithoutFlags       []string       `json:"without_flags,omitempty"`
	LargerThanBytes    int64          `json:"larger_than_bytes,omitempty"`
	SmallerThanBytes   int64          `json:"smaller_than_bytes,omitempty"`
	UIDRanges          []UIDRange     `json:"uid_ranges,omitempty"`
}

type ExportMessagesResult struct {
	Status              string   `json:"status"`
	RunDirectory        string   `json:"run_directory"`
	ManifestPath        string   `json:"manifest_path"`
	Mailboxes           []string `json:"mailboxes"`
	MatchedMessages     int      `json:"matched_messages"`
	ExportedMessages    int      `json:"exported_messages"`
	ExportedAttachments int      `json:"exported_attachments"`
	FailedMessages      int      `json:"failed_messages"`
	FailedAttachments   int      `json:"failed_attachments"`
	FailedMailboxes     int      `json:"failed_mailboxes"`
}

type exportManifest struct {
	Version       int                     `json:"version"`
	CreatedAt     string                  `json:"created_at"`
	Request       ExportMessagesRequest   `json:"request"`
	Mailboxes     []string                `json:"mailboxes"`
	MailboxErrors []exportMailboxError    `json:"mailbox_errors,omitempty"`
	Messages      []exportManifestMessage `json:"messages"`
}

type exportMailboxError struct {
	Mailbox string `json:"mailbox"`
	Error   string `json:"error"`
}

type exportManifestMessage struct {
	Reference   MessageReference     `json:"reference"`
	Status      string               `json:"status"`
	EMLPath     string               `json:"eml_path,omitempty"`
	SizeBytes   int64                `json:"size_bytes,omitempty"`
	SHA256      string               `json:"sha256,omitempty"`
	Attachments []exportManifestFile `json:"attachments,omitempty"`
	Error       string               `json:"error,omitempty"`
}

type exportManifestFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type exportHashWriter struct {
	destination io.Writer
	hash        hash.Hash
	size        int64
}

func (writer *exportHashWriter) Write(data []byte) (int, error) {
	n, err := writer.destination.Write(data)
	if n > 0 {
		writer.size += int64(n)
		_, _ = writer.hash.Write(data[:n])
	}
	return n, err
}

func ExportMessages(ctx context.Context, config Config, request ExportMessagesRequest) (ExportMessagesResult, error) {
	mailboxes, err := normalizeExportMailboxes(request.Mailboxes)
	if err != nil {
		return ExportMessagesResult{}, err
	}
	if err := validateExportRequest(request); err != nil {
		return ExportMessagesResult{}, err
	}
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return ExportMessagesResult{}, ErrExportFailed
	}
	if err := ctx.Err(); err != nil {
		return ExportMessagesResult{}, err
	}

	runDirectory, err := createExportRunDirectory(request.Destination)
	if err != nil {
		return ExportMessagesResult{}, ErrInvalidExport
	}
	manifest := exportManifest{
		Version:   1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Request:   request,
		Mailboxes: mailboxes,
		Messages:  []exportManifestMessage{},
	}
	manifestPath := filepath.Join(runDirectory, "manifest.json")
	if err := writeExportManifest(manifestPath, manifest); err != nil {
		return ExportMessagesResult{}, ErrExportFailed
	}

	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	client, err := dialIMAPClient(ctx, dialExportClient, address, &imapv2client.Options{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.TLSServerName},
		Dialer:    &net.Dialer{Timeout: operationTimeout},
	})
	if err != nil {
		return ExportMessagesResult{}, fmt.Errorf("%w: dial", ErrExportFailed)
	}
	defer client.Close()
	if err := loginIMAPClient(ctx, client, config.Username, config.Password); err != nil {
		return ExportMessagesResult{}, fmt.Errorf("%w: login", ErrExportFailed)
	}

	listed, err := listMailboxData(ctx, client)
	if err != nil {
		return ExportMessagesResult{}, fmt.Errorf("%w: list mailboxes", ErrExportFailed)
	}
	for _, mailbox := range mailboxes {
		if !hasMailbox(listed, mailbox) {
			manifest.MailboxErrors = append(manifest.MailboxErrors, exportMailboxError{Mailbox: mailbox, Error: "mailbox not found"})
		}
	}
	if err := writeExportManifest(manifestPath, manifest); err != nil {
		return ExportMessagesResult{}, ErrExportFailed
	}

	for _, mailbox := range mailboxes {
		if !hasMailbox(listed, mailbox) {
			continue
		}
		if err := exportMailboxWork(ctx, client, mailbox, runDirectory, request, &manifest, manifestPath); err != nil {
			manifest.MailboxErrors = append(manifest.MailboxErrors, exportMailboxError{Mailbox: mailbox, Error: "mailbox export failed"})
			if err := writeExportManifest(manifestPath, manifest); err != nil {
				return ExportMessagesResult{}, ErrExportFailed
			}
		}
	}

	return summarizeExport(runDirectory, manifestPath, manifest), nil
}

func validateExportRequest(request ExportMessagesRequest) error {
	if !filepath.IsAbs(request.Destination) || request.Destination == "" {
		return ErrInvalidExport
	}
	if err := validateDestinationDirectory(request.Destination); err != nil {
		return ErrInvalidExport
	}
	_, _, err := buildSearchCriteria(SearchRequest{
		Since: request.Since, Before: request.Before, SentSince: request.SentSince,
		SentBefore: request.SentBefore, Headers: request.Headers, Body: request.Body,
		Text: request.Text, WithFlags: request.WithFlags, WithoutFlags: request.WithoutFlags,
		LargerThanBytes: request.LargerThanBytes, SmallerThanBytes: request.SmallerThanBytes,
		UIDRanges: request.UIDRanges,
	})
	if err != nil {
		return ErrInvalidExport
	}
	return nil
}

func normalizeExportMailboxes(mailboxes []string) ([]string, error) {
	if len(mailboxes) == 0 {
		return []string{DefaultMailbox}, nil
	}
	result := make([]string, 0, len(mailboxes))
	seen := make(map[string]struct{}, len(mailboxes))
	for _, mailbox := range mailboxes {
		if validateMailboxName(mailbox) != nil {
			return nil, ErrInvalidExport
		}
		if _, exists := seen[mailbox]; exists {
			continue
		}
		seen[mailbox] = struct{}{}
		result = append(result, mailbox)
	}
	return result, nil
}

func createExportRunDirectory(destination string) (string, error) {
	base := "imap-export-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	for suffix := 0; suffix < 100; suffix++ {
		name := base
		if suffix > 0 {
			name += "-" + strconv.Itoa(suffix+1)
		}
		path := filepath.Join(destination, name)
		if err := os.Mkdir(path, 0700); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not create export directory")
}

func exportMailbox(ctx context.Context, client *imapv2client.Client, mailbox, runDirectory string, request ExportMessagesRequest, manifest *exportManifest, manifestPath string) error {
	selected, err := selectIMAPMailbox(ctx, client, mailbox, &imap.SelectOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	criteria, _, err := buildSearchCriteria(SearchRequest{
		Since: request.Since, Before: request.Before, SentSince: request.SentSince,
		SentBefore: request.SentBefore, Headers: request.Headers, Body: request.Body,
		Text: request.Text, WithFlags: request.WithFlags, WithoutFlags: request.WithoutFlags,
		LargerThanBytes: request.LargerThanBytes, SmallerThanBytes: request.SmallerThanBytes,
		UIDRanges: request.UIDRanges,
	})
	if err != nil {
		return err
	}
	searchData, err := searchIMAP(ctx, client, &criteria, nil)
	if err != nil {
		return err
	}
	for _, uid := range searchData.AllUIDs() {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := exportManifestMessage{Reference: MessageReference{Mailbox: mailbox, UIDValidity: selected.UIDValidity, UID: uint32(uid)}, Status: "failed"}
		manifest.Messages = append(manifest.Messages, entry)
		index := len(manifest.Messages) - 1
		if err := exportOneMessage(ctx, client, runDirectory, request.IncludeAttachments, &manifest.Messages[index]); err != nil {
			manifest.Messages[index].Error = "message export failed"
		}
		if err := writeExportManifest(manifestPath, *manifest); err != nil {
			return err
		}
	}
	return nil
}

func exportOneMessage(ctx context.Context, client *imapv2client.Client, runDirectory string, includeAttachments bool, entry *exportManifestMessage) error {
	base := exportMessageBase(entry.Reference)
	emlPath := filepath.Join(runDirectory, base+".eml")
	size, digest, err := writeExportMessage(ctx, client, imap.UID(entry.Reference.UID), emlPath)
	if err != nil {
		return err
	}
	entry.EMLPath = emlPath
	entry.SizeBytes = size
	entry.SHA256 = digest
	entry.Status = "exported"

	if !includeAttachments {
		return nil
	}
	structure, err := fetchExportBodyStructure(ctx, client, imap.UID(entry.Reference.UID))
	if err != nil {
		entry.Status = "partial"
		return err
	}
	attachments := collectAttachments(structure)
	if len(attachments) == 0 {
		return nil
	}
	attachmentDirectory := filepath.Join(runDirectory, base+"-attachments")
	if err := os.Mkdir(attachmentDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		entry.Status = "partial"
		return err
	}
	usedNames := make(map[string]int)
	for _, attachment := range attachments {
		fileEntry := exportManifestFile{Status: "failed"}
		part, path, ok := findAttachmentPart(structure, attachment.PartID)
		if !ok {
			fileEntry.Error = "attachment part unavailable"
			entry.Attachments = append(entry.Attachments, fileEntry)
			entry.Status = "partial"
			continue
		}
		filename, err := attachmentFilename(part, entry.Reference.UID, attachment.PartID)
		if err != nil {
			fileEntry.Error = "attachment filename is unsafe"
			entry.Attachments = append(entry.Attachments, fileEntry)
			entry.Status = "partial"
			continue
		}
		filename = uniqueExportFilename(filename, attachment.PartID, usedNames)
		pathName := filepath.Join(attachmentDirectory, filename)
		size, digest, err := writeExportAttachment(ctx, client, imap.UID(entry.Reference.UID), path, part.Encoding, pathName)
		if err != nil {
			fileEntry.Path = pathName
			fileEntry.Error = "attachment export failed"
			entry.Status = "partial"
		} else {
			fileEntry.Path = pathName
			fileEntry.SizeBytes = size
			fileEntry.SHA256 = digest
			fileEntry.Status = "exported"
		}
		entry.Attachments = append(entry.Attachments, fileEntry)
	}
	if entry.Status == "exported" {
		for _, attachment := range entry.Attachments {
			if attachment.Status != "exported" {
				entry.Status = "partial"
				break
			}
		}
	}
	return nil
}

func fetchExportBodyStructure(ctx context.Context, client *imapv2client.Client, uid imap.UID) (imap.BodyStructure, error) {
	messages, err := fetchIMAPMessages(ctx, client, imap.UIDSetNum(uid), &imap.FetchOptions{
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if err != nil || len(messages) == 0 || messages[0].BodyStructure == nil {
		return nil, errors.New("body structure unavailable")
	}
	return messages[0].BodyStructure, nil
}

func writeExportMessage(ctx context.Context, client *imapv2client.Client, uid imap.UID, finalPath string) (int64, string, error) {
	temporary, err := os.CreateTemp(filepath.Dir(finalPath), ".imap-export-message-*")
	if err != nil {
		return 0, "", err
	}
	temporaryPath := temporary.Name()
	_ = temporary.Chmod(0600)
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	section := &imap.FetchItemBodySection{Peek: true}
	fetchStarted := time.Now()
	command := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}})
	found := false
	writer := &exportHashWriter{destination: temporary, hash: sha256.New()}
	var streamErr error
	for {
		message := command.Next()
		if message == nil {
			break
		}
		for {
			item := message.Next()
			if item == nil {
				break
			}
			body, ok := item.(imapv2client.FetchItemDataBodySection)
			if !ok || body.Literal == nil {
				continue
			}
			found = true
			if streamErr == nil {
				_, streamErr = io.CopyBuffer(writer, contextReader{ctx: ctx, reader: body.Literal}, make([]byte, attachmentBufferSize))
			}
		}
	}
	closeErr := command.Close()
	fetchErr := streamErr
	if fetchErr == nil {
		fetchErr = closeErr
	}
	observability.CompleteIMAP("fetch", fetchStarted, ctx, fetchErr)
	if streamErr != nil || closeErr != nil || !found {
		cleanup()
		if streamErr != nil {
			return 0, "", streamErr
		}
		return 0, "", errors.New("message body unavailable")
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return 0, "", err
	}
	if err := os.Link(temporaryPath, finalPath); err != nil {
		cleanup()
		return 0, "", err
	}
	cleanup()
	return writer.size, fmt.Sprintf("%x", writer.hash.Sum(nil)), nil
}

func writeExportAttachment(ctx context.Context, client *imapv2client.Client, uid imap.UID, path []int, encoding, finalPath string) (int64, string, error) {
	temporary, err := os.CreateTemp(filepath.Dir(finalPath), ".imap-export-attachment-*")
	if err != nil {
		return 0, "", err
	}
	temporaryPath := temporary.Name()
	_ = temporary.Chmod(0600)
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	section := &imap.FetchItemBodySection{Part: path, Peek: true}
	fetchStarted := time.Now()
	command := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}})
	found := false
	writer := &exportHashWriter{destination: temporary, hash: sha256.New()}
	var streamErr error
	for {
		message := command.Next()
		if message == nil {
			break
		}
		for {
			item := message.Next()
			if item == nil {
				break
			}
			body, ok := item.(imapv2client.FetchItemDataBodySection)
			if !ok || body.Literal == nil {
				continue
			}
			found = true
			if streamErr == nil {
				reader, decodeErr := decodeAttachmentReader(contextReader{ctx: ctx, reader: body.Literal}, encoding)
				if decodeErr != nil {
					streamErr = decodeErr
				} else {
					_, streamErr = io.CopyBuffer(writer, reader, make([]byte, attachmentBufferSize))
				}
			}
		}
	}
	closeErr := command.Close()
	fetchErr := streamErr
	if fetchErr == nil {
		fetchErr = closeErr
	}
	observability.CompleteIMAP("fetch", fetchStarted, ctx, fetchErr)
	if streamErr != nil || closeErr != nil || !found {
		cleanup()
		return 0, "", errors.New("attachment body unavailable")
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return 0, "", err
	}
	if err := os.Link(temporaryPath, finalPath); err != nil {
		cleanup()
		return 0, "", err
	}
	cleanup()
	return writer.size, fmt.Sprintf("%x", writer.hash.Sum(nil)), nil
}

func exportMessageBase(reference MessageReference) string {
	mailbox := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, reference.Mailbox)
	mailbox = strings.Trim(mailbox, "_")
	if mailbox == "" {
		mailbox = "mailbox"
	}
	return fmt.Sprintf("%s-uidvalidity-%d-uid-%d", mailbox, reference.UIDValidity, reference.UID)
}

func uniqueExportFilename(filename, partID string, used map[string]int) string {
	if _, exists := used[filename]; !exists {
		used[filename] = 1
		return filename
	}
	base := strings.TrimSuffix(filename, filepath.Ext(filename))
	extension := filepath.Ext(filename)
	candidate := base + "-part-" + strings.ReplaceAll(partID, ".", "-") + extension
	if _, exists := used[candidate]; !exists {
		used[candidate] = 1
		return candidate
	}
	for index := 2; ; index++ {
		candidate = fmt.Sprintf("%s-%d%s", base, index, extension)
		if _, exists := used[candidate]; !exists {
			used[candidate] = 1
			return candidate
		}
	}
}

func writeExportManifest(path string, manifest exportManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".imap-export-manifest-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	_ = temporary.Chmod(0600)
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func summarizeExport(runDirectory, manifestPath string, manifest exportManifest) ExportMessagesResult {
	result := ExportMessagesResult{RunDirectory: runDirectory, ManifestPath: manifestPath, Mailboxes: append([]string(nil), manifest.Mailboxes...)}
	result.FailedMailboxes = len(manifest.MailboxErrors)
	for _, message := range manifest.Messages {
		result.MatchedMessages++
		if message.Status == "exported" {
			result.ExportedMessages++
		} else {
			result.FailedMessages++
		}
		for _, attachment := range message.Attachments {
			if attachment.Status == "exported" {
				result.ExportedAttachments++
			} else {
				result.FailedAttachments++
			}
		}
	}
	if result.FailedMailboxes == 0 && result.FailedMessages == 0 && result.FailedAttachments == 0 {
		result.Status = "complete"
	} else if result.FailedMailboxes == len(manifest.Mailboxes) {
		result.Status = "failed"
	} else {
		result.Status = "partial"
	}
	return result
}
