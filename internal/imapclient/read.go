package imapclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"mime/quotedprintable"
)

const maxReadTextBytes = 128 * 1024

var (
	ErrInvalidRead = errors.New("read request is invalid")
	ErrReadFailed  = errors.New("IMAP read failed")
	dialReadClient = imapv2client.DialTLS
)

// ReadMessagesRequest identifies one to ten messages from one mailbox.
type ReadMessagesRequest struct {
	Messages []MessageReference `json:"messages"`
}

type MessageReference struct {
	Mailbox     string `json:"mailbox"`
	UIDValidity uint32 `json:"uid_validity"`
	UID         uint32 `json:"uid"`
}

type ReadMessagesResult struct {
	Messages []ReadMessage `json:"messages"`
}

type ReadMessage struct {
	Reference   MessageReference     `json:"reference"`
	Text        string               `json:"text"`
	Source      string               `json:"source"`
	Truncated   bool                 `json:"truncated"`
	Attachments []AttachmentMetadata `json:"attachments"`
	Error       string               `json:"error"`
}

type AttachmentMetadata struct {
	PartID      string `json:"part_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

type selectedTextPart struct {
	path     []int
	source   string
	encoding string
	charset  string
}

func ReadMessages(ctx context.Context, config Config, request ReadMessagesRequest) (ReadMessagesResult, error) {
	if err := validateReadRequest(request); err != nil {
		return ReadMessagesResult{}, err
	}
	result := ReadMessagesResult{Messages: make([]ReadMessage, len(request.Messages))}
	for i, reference := range request.Messages {
		result.Messages[i] = ReadMessage{
			Reference:   reference,
			Attachments: []AttachmentMetadata{},
		}
	}
	if err := ctx.Err(); err != nil {
		return ReadMessagesResult{}, err
	}
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return ReadMessagesResult{}, ErrReadFailed
	}

	mailbox := request.Messages[0].Mailbox
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	client, err := dialIMAPClient(ctx, dialReadClient, address, &imapv2client.Options{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.TLSServerName},
		Dialer:    &net.Dialer{Timeout: operationTimeout},
	})
	if err != nil {
		return ReadMessagesResult{}, fmt.Errorf("%w: dial", ErrReadFailed)
	}
	defer client.Close()

	if err := loginIMAPClient(ctx, client, config.Username, config.Password); err != nil {
		return ReadMessagesResult{}, fmt.Errorf("%w: login", ErrReadFailed)
	}
	selected, err := selectIMAPMailbox(ctx, client, mailbox, &imap.SelectOptions{ReadOnly: true})
	if err != nil {
		return ReadMessagesResult{}, fmt.Errorf("%w: select", ErrReadFailed)
	}

	validUIDs := make([]imap.UID, 0, len(request.Messages))
	validIndexes := make(map[imap.UID][]int)
	for i, reference := range request.Messages {
		if reference.UIDValidity != selected.UIDValidity {
			result.Messages[i].Error = "stale message reference"
			continue
		}
		uid := imap.UID(reference.UID)
		if _, exists := validIndexes[uid]; !exists {
			validUIDs = append(validUIDs, uid)
		}
		validIndexes[uid] = append(validIndexes[uid], i)
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
		return ReadMessagesResult{}, fmt.Errorf("%w: body structure", ErrReadFailed)
	}
	byUID := make(map[imap.UID]*imapv2client.FetchMessageBuffer, len(structureFetch))
	for _, message := range structureFetch {
		byUID[message.UID] = message
	}

	for i, reference := range request.Messages {
		if result.Messages[i].Error != "" {
			continue
		}
		message := byUID[imap.UID(reference.UID)]
		if message == nil {
			result.Messages[i].Error = "message not found"
			continue
		}

		result.Messages[i].Attachments = collectAttachments(message.BodyStructure)
		part, ok := chooseTextPart(message.BodyStructure)
		if !ok {
			result.Messages[i].Error = "no readable text body"
			continue
		}

		section := &imap.FetchItemBodySection{Part: part.path, Peek: true}
		bodyFetch, err := fetchIMAPMessages(ctx, client, imap.UIDSetNum(imap.UID(reference.UID)), &imap.FetchOptions{
			BodySection: []*imap.FetchItemBodySection{section},
		})
		if err != nil || len(bodyFetch) == 0 {
			result.Messages[i].Error = "message body unavailable"
			continue
		}
		encoded := bodyFetch[0].FindBodySection(section)
		decoded, err := decodeTextBody(encoded, part.encoding, part.charset)
		if err != nil {
			result.Messages[i].Error = "message body could not be decoded"
			continue
		}
		if part.source == "html" {
			decoded = htmlToText(decoded)
		}
		result.Messages[i].Source = part.source
		result.Messages[i].Text, result.Messages[i].Truncated = boundReadText(decoded)
	}

	return result, nil
}

func validateReadRequest(request ReadMessagesRequest) error {
	if len(request.Messages) < 1 || len(request.Messages) > 10 {
		return ErrInvalidRead
	}
	mailbox := ""
	for _, reference := range request.Messages {
		if err := validateMailboxName(reference.Mailbox); err != nil || reference.UIDValidity == 0 || reference.UID == 0 {
			return ErrInvalidRead
		}
		if mailbox == "" {
			mailbox = reference.Mailbox
		} else if reference.Mailbox != mailbox {
			return ErrInvalidRead
		}
	}
	return nil
}

func chooseTextPart(structure imap.BodyStructure) (selectedTextPart, bool) {
	if structure == nil {
		return selectedTextPart{}, false
	}
	var plain, htmlPart *selectedTextPart
	structure.Walk(func(path []int, part imap.BodyStructure) bool {
		if isAttachmentPart(part) {
			return false
		}
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok || single.MessageRFC822 != nil {
			return true
		}
		candidate := selectedTextPart{
			path:     append([]int(nil), path...),
			encoding: single.Encoding,
			charset:  bodyCharset(single.Params),
		}
		switch strings.ToLower(single.MediaType()) {
		case "text/plain":
			candidate.source = "plain"
			if plain == nil {
				plain = &candidate
			}
		case "text/html":
			candidate.source = "html"
			if htmlPart == nil {
				htmlPart = &candidate
			}
		}
		return true
	})
	if plain != nil {
		return *plain, true
	}
	if htmlPart != nil {
		return *htmlPart, true
	}
	return selectedTextPart{}, false
}

func collectAttachments(structure imap.BodyStructure) []AttachmentMetadata {
	attachments := []AttachmentMetadata{}
	if structure == nil {
		return attachments
	}
	structure.Walk(func(path []int, part imap.BodyStructure) bool {
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return !isAttachmentPart(part)
		}
		filename := single.Filename()
		if isAttachmentPart(part) {
			attachments = append(attachments, AttachmentMetadata{
				PartID:      formatPartID(path),
				Filename:    filename,
				ContentType: single.MediaType(),
				SizeBytes:   int64(single.Size),
			})
			return false
		}
		return true
	})
	return attachments
}

func isAttachmentPart(part imap.BodyStructure) bool {
	if disposition := part.Disposition(); disposition != nil && strings.EqualFold(disposition.Value, "attachment") {
		return true
	}
	if single, ok := part.(*imap.BodyStructureSinglePart); ok {
		return single.Filename() != ""
	}
	return false
}

func formatPartID(path []int) string {
	values := make([]string, len(path))
	for i, value := range path {
		values[i] = strconv.Itoa(value)
	}
	return strings.Join(values, ".")
}

func bodyCharset(params map[string]string) string {
	for key, value := range params {
		if strings.EqualFold(key, "charset") {
			return value
		}
	}
	return ""
}

func decodeTextBody(encoded []byte, transferEncoding, charset string) (string, error) {
	var decoded []byte
	var err error
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "", "7bit", "8bit", "binary":
		decoded = encoded
	case "base64":
		decoded, err = base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(encoded)))
	case "quoted-printable":
		reader := quotedprintable.NewReader(bytes.NewReader(encoded))
		decoded, err = io.ReadAll(reader)
	default:
		return "", errors.New("unsupported transfer encoding")
	}
	if err != nil {
		return "", err
	}
	return convertCharset(decoded, charset), nil
}

func convertCharset(value []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8":
		return strings.ToValidUTF8(string(value), "\uFFFD")
	case "us-ascii", "ascii":
		return asciiString(value)
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1":
		return latin1String(value)
	case "windows-1252", "cp1252":
		return windows1252String(value)
	default:
		return strings.ToValidUTF8(string(value), "\uFFFD")
	}
}

func asciiString(value []byte) string {
	result := make([]rune, 0, len(value))
	for _, b := range value {
		if b < utf8.RuneSelf {
			result = append(result, rune(b))
		} else {
			result = append(result, '\uFFFD')
		}
	}
	return string(result)
}

func latin1String(value []byte) string {
	result := make([]rune, len(value))
	for i, b := range value {
		result[i] = rune(b)
	}
	return string(result)
}

func windows1252String(value []byte) string {
	const replacements = "€\uFFFD‚ƒ„…†‡ˆ‰Š‹Œ\uFFFDŽ\uFFFD\uFFFD‘’“”•–—˜š›œ\uFFFDžŸ"
	result := make([]rune, 0, len(value))
	for _, b := range value {
		if b >= 0x80 && b <= 0x9F {
			result = append(result, []rune(replacements)[b-0x80])
		} else {
			result = append(result, rune(b))
		}
	}
	return string(result)
}

func htmlToText(value string) string {
	var output strings.Builder
	inTag := false
	skipDepth := 0
	for i := 0; i < len(value); i++ {
		if value[i] == '<' {
			end := strings.IndexByte(value[i:], '>')
			if end < 0 {
				break
			}
			tag := strings.TrimSpace(strings.ToLower(value[i+1 : i+end]))
			name := strings.Fields(strings.TrimPrefix(tag, "/"))
			tagName := ""
			if len(name) > 0 {
				tagName = strings.Trim(name[0], "!?/")
			}
			closing := strings.HasPrefix(tag, "/")
			if tagName == "script" || tagName == "style" {
				if closing {
					if skipDepth > 0 {
						skipDepth--
					}
				} else {
					skipDepth++
				}
			}
			if skipDepth == 0 && (tagName == "br" || tagName == "p" || tagName == "div" || tagName == "li" || tagName == "tr" || strings.HasPrefix(tagName, "h")) {
				output.WriteByte('\n')
			}
			i += end
			inTag = false
			continue
		}
		if skipDepth == 0 && !inTag {
			output.WriteByte(value[i])
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(output.String())), " ")
}

func boundReadText(value string) (string, bool) {
	if len(value) <= maxReadTextBytes {
		return value, false
	}
	value = value[:maxReadTextBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}
