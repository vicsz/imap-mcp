package imapclient

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
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
	"mime/quotedprintable"
)

const (
	maxAttachmentBytes      int64 = 25 * 1024 * 1024
	maxDownloadRequestBytes int64 = 100 * 1024 * 1024
	attachmentBufferSize          = 32 * 1024
)

var (
	ErrInvalidDownload = errors.New("download request is invalid")
	ErrDownloadFailed  = errors.New("IMAP attachment download failed")
	dialDownloadClient = imapv2client.DialTLS
)

type DownloadAttachmentsRequest struct {
	Destination string                `json:"destination"`
	Attachments []AttachmentReference `json:"attachments"`
}

type AttachmentReference struct {
	Message MessageReference `json:"message"`
	PartID  string           `json:"part_id"`
}

type DownloadAttachmentsResult struct {
	Attachments []DownloadedAttachment `json:"attachments"`
}

type DownloadedAttachment struct {
	Reference AttachmentReference `json:"reference"`
	Status    string              `json:"status"`
	SavedPath string              `json:"saved_path"`
	SizeBytes int64               `json:"size_bytes"`
	SHA256    string              `json:"sha256"`
	Error     string              `json:"error"`
}

func DownloadAttachments(ctx context.Context, config Config, request DownloadAttachmentsRequest) (DownloadAttachmentsResult, error) {
	if err := validateDownloadRequest(request); err != nil {
		return DownloadAttachmentsResult{}, err
	}
	result := DownloadAttachmentsResult{Attachments: make([]DownloadedAttachment, len(request.Attachments))}
	for i, reference := range request.Attachments {
		result.Attachments[i] = DownloadedAttachment{
			Reference: reference,
			Status:    "failed",
		}
	}
	if err := ctx.Err(); err != nil {
		return DownloadAttachmentsResult{}, err
	}
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return DownloadAttachmentsResult{}, ErrDownloadFailed
	}

	mailbox := request.Attachments[0].Message.Mailbox
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	client, err := dialIMAPClient(ctx, dialDownloadClient, address, &imapv2client.Options{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.TLSServerName},
		Dialer:    &net.Dialer{Timeout: operationTimeout},
	})
	if err != nil {
		return DownloadAttachmentsResult{}, fmt.Errorf("%w: dial", ErrDownloadFailed)
	}
	defer client.Close()

	if err := loginIMAPClient(ctx, client, config.Username, config.Password); err != nil {
		return DownloadAttachmentsResult{}, fmt.Errorf("%w: login", ErrDownloadFailed)
	}
	selected, err := selectIMAPMailbox(ctx, client, mailbox, &imap.SelectOptions{ReadOnly: true})
	if err != nil {
		return DownloadAttachmentsResult{}, fmt.Errorf("%w: select", ErrDownloadFailed)
	}

	validUIDs := make([]imap.UID, 0, len(request.Attachments))
	seenUIDs := make(map[imap.UID]struct{})
	for i, reference := range request.Attachments {
		if reference.Message.UIDValidity != selected.UIDValidity {
			result.Attachments[i].Error = "stale message reference"
			continue
		}
		uid := imap.UID(reference.Message.UID)
		if _, seen := seenUIDs[uid]; !seen {
			seenUIDs[uid] = struct{}{}
			validUIDs = append(validUIDs, uid)
		}
	}
	if len(validUIDs) == 0 {
		return result, nil
	}

	structureFetch, err := fetchIMAPMessages(ctx, client, imap.UIDSetNum(validUIDs...), &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
	})
	if err != nil {
		return DownloadAttachmentsResult{}, fmt.Errorf("%w: body structure", ErrDownloadFailed)
	}
	byUID := make(map[imap.UID]*imapv2client.FetchMessageBuffer, len(structureFetch))
	for _, message := range structureFetch {
		byUID[message.UID] = message
	}

	var totalBytes int64
	for i, reference := range request.Attachments {
		if result.Attachments[i].Error != "" {
			continue
		}
		if ctx.Err() != nil {
			result.Attachments[i].Error = "download canceled"
			continue
		}
		message := byUID[imap.UID(reference.Message.UID)]
		if message == nil {
			result.Attachments[i].Error = "message not found"
			continue
		}
		part, path, ok := findAttachmentPart(message.BodyStructure, reference.PartID)
		if !ok {
			result.Attachments[i].Error = "part is not a downloadable attachment"
			continue
		}
		filename, err := attachmentFilename(part, reference.Message.UID, reference.PartID)
		if err != nil {
			result.Attachments[i].Error = "attachment filename is unsafe"
			continue
		}
		finalPath := filepath.Join(request.Destination, filename)
		if err := rejectExistingDestination(finalPath); err != nil {
			result.Attachments[i].Error = "destination file already exists"
			continue
		}

		temporary, err := os.CreateTemp(request.Destination, ".imap-mail-mcp-*")
		if err != nil {
			result.Attachments[i].Error = "could not create temporary file"
			continue
		}
		temporaryPath := temporary.Name()
		_ = temporary.Chmod(0600)
		cleanup := func() {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}

		section := &imap.FetchItemBodySection{Part: path, Peek: true}
		remaining := maxDownloadRequestBytes - totalBytes
		if remaining > maxAttachmentBytes {
			remaining = maxAttachmentBytes
		}
		stats, streamErr := streamAttachmentPart(ctx, client, imap.UID(reference.Message.UID), section, temporary, part.Encoding, remaining)
		closeErr := temporary.Close()
		if streamErr != nil || closeErr != nil {
			cleanup()
			if errors.Is(streamErr, errAttachmentTooLarge) || errors.Is(streamErr, errDownloadRequestTooLarge) {
				result.Attachments[i].Error = "attachment exceeds download size limit"
			} else if errors.Is(streamErr, errDownloadCanceled) || errors.Is(ctx.Err(), context.Canceled) {
				result.Attachments[i].Error = "download canceled"
			} else {
				result.Attachments[i].Error = "attachment download failed"
			}
			continue
		}
		if err := os.Link(temporaryPath, finalPath); err != nil {
			cleanup()
			result.Attachments[i].Error = "destination file already exists"
			continue
		}
		cleanup()
		totalBytes += stats.size
		result.Attachments[i].Status = "downloaded"
		result.Attachments[i].SavedPath = finalPath
		result.Attachments[i].SizeBytes = stats.size
		result.Attachments[i].SHA256 = stats.sha256
	}

	return result, nil
}

func validateDownloadRequest(request DownloadAttachmentsRequest) error {
	if !filepath.IsAbs(request.Destination) || len(request.Attachments) < 1 || len(request.Attachments) > 10 {
		return ErrInvalidDownload
	}
	if err := validateDestinationDirectory(request.Destination); err != nil {
		return ErrInvalidDownload
	}
	mailbox := ""
	for _, reference := range request.Attachments {
		if validateMailboxName(reference.Message.Mailbox) != nil || reference.Message.UIDValidity == 0 || reference.Message.UID == 0 {
			return ErrInvalidDownload
		}
		if mailbox == "" {
			mailbox = reference.Message.Mailbox
		} else if reference.Message.Mailbox != mailbox {
			return ErrInvalidDownload
		}
		if _, err := parsePartID(reference.PartID); err != nil {
			return ErrInvalidDownload
		}
	}
	return nil
}

func validateDestinationDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidDownload
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ErrInvalidDownload
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return nil
	}
	relative, err := filepath.Rel(workingDirectory, abs)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return ErrInvalidDownload
	}
	return nil
}

func parsePartID(value string) ([]int, error) {
	if value == "" {
		return nil, ErrInvalidDownload
	}
	parts := strings.Split(value, ".")
	path := make([]int, len(parts))
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return nil, ErrInvalidDownload
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 1 {
			return nil, ErrInvalidDownload
		}
		path[i] = number
	}
	return path, nil
}

func findAttachmentPart(structure imap.BodyStructure, partID string) (*imap.BodyStructureSinglePart, []int, bool) {
	path, err := parsePartID(partID)
	if err != nil || structure == nil {
		return nil, nil, false
	}
	var found *imap.BodyStructureSinglePart
	var foundPath []int
	structure.Walk(func(currentPath []int, part imap.BodyStructure) bool {
		if formatPartID(currentPath) == partID {
			single, ok := part.(*imap.BodyStructureSinglePart)
			if ok && isAttachmentPart(part) {
				found = single
				foundPath = append([]int(nil), path...)
			}
			return false
		}
		return !isAttachmentPart(part)
	})
	return found, foundPath, found != nil
}

func attachmentFilename(part *imap.BodyStructureSinglePart, uid uint32, partID string) (string, error) {
	filename := strings.TrimSpace(part.Filename())
	if filename == "" {
		return "attachment-" + strconv.FormatUint(uint64(uid), 10) + "-" + strings.ReplaceAll(partID, ".", "-") + ".bin", nil
	}
	if strings.ContainsAny(filename, `/\\`) || filename == "." || filename == ".." {
		return "", ErrInvalidDownload
	}
	for _, character := range filename {
		if unicode.IsControl(character) {
			return "", ErrInvalidDownload
		}
	}
	return filename, nil
}

func rejectExistingDestination(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() || info.IsDir() {
			return os.ErrExist
		}
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

var (
	errAttachmentTooLarge      = errors.New("attachment exceeds per-file limit")
	errDownloadRequestTooLarge = errors.New("attachment exceeds request limit")
	errDownloadCanceled        = errors.New("attachment download canceled")
)

type attachmentStats struct {
	size   int64
	sha256 string
}

func streamAttachmentPart(ctx context.Context, client *imapv2client.Client, uid imap.UID, section *imap.FetchItemBodySection, destination io.Writer, transferEncoding string, limit int64) (statsResult attachmentStats, resultErr error) {
	started := time.Now()
	defer func() { observability.CompleteIMAP("fetch", started, ctx, resultErr) }()
	command := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}})
	var streamErr error
	stats := attachmentStats{}
	found := false
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
				reader, err := decodeAttachmentReader(contextReader{ctx: ctx, reader: body.Literal}, transferEncoding)
				if err != nil {
					streamErr = err
				} else {
					stats, streamErr = copyAttachmentStats(reader, destination, limit)
				}
			}
		}
	}
	closeErr := command.Close()
	if streamErr != nil {
		return attachmentStats{}, streamErr
	}
	if closeErr != nil || !found {
		return attachmentStats{}, ErrDownloadFailed
	}
	return stats, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(p []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, errDownloadCanceled
	}
	return reader.reader.Read(p)
}

func decodeAttachmentReader(reader io.Reader, transferEncoding string) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "", "7bit", "8bit", "binary":
		return reader, nil
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, newMIMEWhitespaceReader(reader)), nil
	case "quoted-printable":
		return quotedprintable.NewReader(reader), nil
	default:
		return nil, errors.New("unsupported transfer encoding")
	}
}

type mimeWhitespaceReader struct {
	reader io.Reader
	buffer []byte
}

func newMIMEWhitespaceReader(reader io.Reader) io.Reader {
	return &mimeWhitespaceReader{reader: reader}
}

func (reader *mimeWhitespaceReader) Read(p []byte) (int, error) {
	for len(reader.buffer) == 0 {
		chunk := make([]byte, attachmentBufferSize)
		n, err := reader.reader.Read(chunk)
		for _, value := range chunk[:n] {
			if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
				reader.buffer = append(reader.buffer, value)
			}
		}
		if len(reader.buffer) > 0 {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	n := copy(p, reader.buffer)
	reader.buffer = reader.buffer[n:]
	return n, nil
}

type attachmentSink struct {
	destination io.Writer
	hash        hash.Hash
	limit       int64
	written     int64
}

func (sink *attachmentSink) Write(p []byte) (int, error) {
	if int64(len(p)) > sink.limit-sink.written {
		return 0, errAttachmentTooLarge
	}
	n, err := sink.destination.Write(p)
	sink.written += int64(n)
	if n > 0 {
		_, _ = sink.hash.Write(p[:n])
	}
	return n, err
}

func copyAttachment(reader io.Reader, destination io.Writer, limit int64) error {
	_, err := copyAttachmentStats(reader, destination, limit)
	return err
}

func copyAttachmentStats(reader io.Reader, destination io.Writer, limit int64) (attachmentStats, error) {
	sink := &attachmentSink{destination: destination, hash: sha256.New(), limit: limit}
	buffer := make([]byte, attachmentBufferSize)
	_, err := io.CopyBuffer(sink, reader, buffer)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return attachmentStats{}, errDownloadCanceled
		}
		return attachmentStats{}, err
	}
	return attachmentStats{size: sink.written, sha256: fmt.Sprintf("%x", sink.hash.Sum(nil))}, nil
}
