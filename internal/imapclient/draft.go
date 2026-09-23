package imapclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	netmail "net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	gomail "github.com/emersion/go-message/mail"
	"imap-mail-mcp/internal/observability"
)

const (
	maxDraftTextBytes  = 128 * 1024
	maxDraftSubject    = 500
	maxDraftRecipients = 20
)

var (
	ErrInvalidDraft = errors.New("draft request is invalid")
	ErrDraftFailed  = errors.New("draft could not be created")
	dialDraftClient = imapv2client.DialTLS
)

// CreateDraftRequest has mutually exclusive new and reply branches selected by Mode.
// Pointer fields preserve whether optional branch fields were supplied.
type CreateDraftRequest struct {
	Mode    string            `json:"mode"`
	To      *[]string         `json:"to,omitempty"`
	Cc      *[]string         `json:"cc,omitempty"`
	Bcc     *[]string         `json:"bcc,omitempty"`
	Subject *string           `json:"subject,omitempty"`
	Text    string            `json:"text"`
	ReplyTo *MessageReference `json:"reply_to,omitempty"`
}

type CreateDraftResult struct {
	Status         string            `json:"status"`
	Mode           string            `json:"mode"`
	MessageID      string            `json:"message_id"`
	Threaded       *bool             `json:"threaded,omitempty"`
	DraftReference *MessageReference `json:"draft_reference,omitempty"`
}

type validatedDraft struct {
	mode    string
	to      []*netmail.Address
	cc      []*netmail.Address
	bcc     []*netmail.Address
	subject string
	text    string
	replyTo *MessageReference
}

func CreateDraft(ctx context.Context, config Config, request CreateDraftRequest) (CreateDraftResult, error) {
	draft, err := validateCreateDraftRequest(request)
	if err != nil {
		return CreateDraftResult{}, err
	}
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return CreateDraftResult{}, ErrInvalidConfiguration
	}

	fromValue := config.FromAddress
	if fromValue == "" {
		fromValue = config.Username
	}
	from, err := parseDraftAddress(fromValue)
	if err != nil {
		return CreateDraftResult{}, ErrInvalidConfiguration
	}

	if err := ctx.Err(); err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	client, err := dialIMAPClient(ctx, dialDraftClient, address, &imapv2client.Options{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.TLSServerName},
		Dialer:    &net.Dialer{Timeout: operationTimeout},
	})
	if err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}
	stopCloseOnCancel := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopCloseOnCancel()
	defer client.Close()

	if err := ctx.Err(); err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}
	if err := loginIMAPClient(ctx, client, config.Username, config.Password); err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}
	draftsMailbox, err := findDraftsMailbox(ctx, client)
	if err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}
	if err := ctx.Err(); err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}

	threaded := false
	var thread *draftThread
	if draft.mode == "reply" {
		thread, err = fetchReplyThread(ctx, client, *draft.replyTo)
		if err != nil {
			return CreateDraftResult{}, ErrDraftFailed
		}
		threaded = thread.messageID != ""
		draft.to = []*netmail.Address{thread.to}
		draft.subject = replySubject(thread.subject)
	}

	rawMessage, messageID, err := buildDraftMessage(config, from, draft, thread)
	if err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}
	result := CreateDraftResult{
		Status:    "unknown",
		Mode:      draft.mode,
		MessageID: messageID,
	}
	if draft.mode == "reply" {
		result.Threaded = &threaded
	}

	if err := ctx.Err(); err != nil {
		return CreateDraftResult{}, ErrDraftFailed
	}
	appendStarted := time.Now()
	appendCommand := client.Append(draftsMailbox, int64(len(rawMessage)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft}})
	written, writeErr := appendCommand.Write(rawMessage)
	closeErr := appendCommand.Close()
	appendData, waitErr := appendCommand.Wait()
	appendErr := writeErr
	if appendErr == nil && written != len(rawMessage) {
		appendErr = io.ErrShortWrite
	}
	if appendErr == nil {
		appendErr = closeErr
	}
	if appendErr == nil {
		appendErr = waitErr
	}
	observability.CompleteIMAP("append", appendStarted, ctx, appendErr)
	if appendWasRejected(writeErr) || appendWasRejected(closeErr) || appendWasRejected(waitErr) {
		return CreateDraftResult{}, ErrDraftFailed
	}
	if writeErr != nil || written != len(rawMessage) || closeErr != nil {
		return result, nil
	}
	if waitErr != nil {
		return result, nil
	}

	result.Status = "created"
	if appendData != nil && appendData.UIDValidity != 0 && appendData.UID != 0 {
		result.DraftReference = &MessageReference{
			Mailbox:     draftsMailbox,
			UIDValidity: appendData.UIDValidity,
			UID:         uint32(appendData.UID),
		}
	}
	return result, nil
}

func validateCreateDraftRequest(request CreateDraftRequest) (validatedDraft, error) {
	if request.Text == "" || !utf8.ValidString(request.Text) || len(request.Text) > maxDraftTextBytes {
		return validatedDraft{}, ErrInvalidDraft
	}
	draft := validatedDraft{mode: request.Mode, text: request.Text, replyTo: request.ReplyTo}
	switch request.Mode {
	case "new":
		if request.To == nil || request.Subject == nil || request.ReplyTo != nil {
			return validatedDraft{}, ErrInvalidDraft
		}
		if strings.TrimSpace(*request.Subject) == "" || utf8.RuneCountInString(*request.Subject) > maxDraftSubject || hasControlCharacters(*request.Subject) {
			return validatedDraft{}, ErrInvalidDraft
		}
		var err error
		draft.to, err = parseDraftAddresses(*request.To)
		if err != nil || len(draft.to) == 0 {
			return validatedDraft{}, ErrInvalidDraft
		}
		if request.Cc != nil {
			draft.cc, err = parseDraftAddresses(*request.Cc)
			if err != nil {
				return validatedDraft{}, ErrInvalidDraft
			}
		}
		if request.Bcc != nil {
			draft.bcc, err = parseDraftAddresses(*request.Bcc)
			if err != nil {
				return validatedDraft{}, ErrInvalidDraft
			}
		}
		if len(draft.to)+len(draft.cc)+len(draft.bcc) > maxDraftRecipients {
			return validatedDraft{}, ErrInvalidDraft
		}
		draft.subject = *request.Subject
	case "reply":
		if request.ReplyTo == nil || request.To != nil || request.Cc != nil || request.Bcc != nil || request.Subject != nil {
			return validatedDraft{}, ErrInvalidDraft
		}
		if validateMailboxName(request.ReplyTo.Mailbox) != nil || request.ReplyTo.UIDValidity == 0 || request.ReplyTo.UID == 0 {
			return validatedDraft{}, ErrInvalidDraft
		}
	default:
		return validatedDraft{}, ErrInvalidDraft
	}
	return draft, nil
}

func parseDraftAddresses(values []string) ([]*netmail.Address, error) {
	addresses := make([]*netmail.Address, 0, len(values))
	for _, value := range values {
		address, err := parseDraftAddress(value)
		if err != nil {
			return nil, ErrInvalidDraft
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

func parseDraftAddress(value string) (*netmail.Address, error) {
	if value == "" || hasControlCharacters(value) {
		return nil, ErrInvalidDraft
	}
	address, err := netmail.ParseAddress(value)
	if err != nil || address.Address == "" || hasControlCharacters(address.Address) {
		return nil, ErrInvalidDraft
	}
	return address, nil
}

func hasControlCharacters(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func findDraftsMailbox(ctx context.Context, client *imapv2client.Client) (string, error) {
	listed, err := listIMAPMailboxes(ctx, client, "", "*", &imap.ListOptions{ReturnSpecialUse: true})
	if err != nil {
		return "", ErrDraftFailed
	}
	var drafts []string
	for _, mailbox := range listed {
		if mailbox == nil || mailbox.Mailbox == "" || hasMailboxAttribute(mailbox.Attrs, "\\Noselect") {
			continue
		}
		if hasMailboxAttribute(mailbox.Attrs, string(imap.MailboxAttrDrafts)) {
			drafts = append(drafts, mailbox.Mailbox)
		}
	}
	if len(drafts) != 1 {
		return "", ErrDraftFailed
	}
	return drafts[0], nil
}

func hasMailboxAttribute(attributes []imap.MailboxAttr, expected string) bool {
	for _, attribute := range attributes {
		if strings.EqualFold(string(attribute), expected) {
			return true
		}
	}
	return false
}

type draftThread struct {
	to         *netmail.Address
	subject    string
	messageID  string
	references []string
}

func fetchReplyThread(ctx context.Context, client *imapv2client.Client, reference MessageReference) (*draftThread, error) {
	selected, err := selectIMAPMailbox(ctx, client, reference.Mailbox, &imap.SelectOptions{ReadOnly: true})
	if err != nil || selected == nil || selected.UIDValidity != reference.UIDValidity {
		return nil, ErrDraftFailed
	}
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"From", "Reply-To", "Subject", "Message-ID", "References"},
		Peek:         true,
	}
	messages, err := fetchIMAPMessages(ctx, client, imap.UIDSetNum(imap.UID(reference.UID)), &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{section},
	})
	if err != nil || len(messages) != 1 || messages[0].UID != imap.UID(reference.UID) {
		return nil, ErrDraftFailed
	}
	rawHeaders := messages[0].FindBodySection(section)
	if len(rawHeaders) == 0 {
		return nil, ErrDraftFailed
	}
	parsed, err := netmail.ReadMessage(bytes.NewReader(rawHeaders))
	if err != nil {
		return nil, ErrDraftFailed
	}
	header := gomail.HeaderFromMap(map[string][]string(parsed.Header))
	replyTo, err := header.AddressList("Reply-To")
	if err != nil || len(replyTo) != 1 {
		replyTo = nil
	}
	if len(replyTo) == 0 {
		replyTo, err = header.AddressList("From")
		if err != nil || len(replyTo) != 1 {
			return nil, ErrDraftFailed
		}
	}
	subject, err := header.Subject()
	if err != nil {
		subject = ""
	}
	subject = replySubject(subject)
	messageID, err := header.MessageID()
	if err != nil {
		messageID = ""
	}
	thread := &draftThread{to: replyTo[0], subject: subject, messageID: messageID}
	if messageID != "" {
		if references, referencesErr := header.MsgIDList("References"); referencesErr == nil {
			thread.references = references
		}
	}
	return thread, nil
}

func replySubject(subject string) string {
	var clean strings.Builder
	for _, character := range subject {
		if unicode.IsControl(character) {
			clean.WriteRune(' ')
		} else {
			clean.WriteRune(character)
		}
	}
	subject = strings.Join(strings.Fields(clean.String()), " ")
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		if subject == "" {
			return "Re:"
		}
		return "Re: " + subject
	}
	return subject
}

func buildDraftMessage(config Config, from *netmail.Address, draft validatedDraft, thread *draftThread) ([]byte, string, error) {
	var header gomail.Header
	header.SetAddressList("From", []*gomail.Address{from})
	header.SetAddressList("To", draft.to)
	header.SetAddressList("Cc", draft.cc)
	header.SetAddressList("Bcc", draft.bcc)
	header.SetSubject(draft.subject)
	header.SetDate(time.Now())
	header.Set("MIME-Version", "1.0")
	header.Set("Content-Type", "text/plain; charset=utf-8")
	if err := header.GenerateMessageIDWithHostname(config.TLSServerName); err != nil {
		return nil, "", ErrDraftFailed
	}
	messageIDValue, err := header.MessageID()
	if err != nil || messageIDValue == "" {
		return nil, "", ErrDraftFailed
	}
	if thread != nil && thread.messageID != "" {
		header.SetMsgIDList("In-Reply-To", []string{thread.messageID})
		references := append([]string(nil), thread.references...)
		if len(references) == 0 || references[len(references)-1] != thread.messageID {
			references = append(references, thread.messageID)
		}
		header.SetMsgIDList("References", references)
	}

	var message bytes.Buffer
	bodyWriter, err := gomail.CreateSingleInlineWriter(&message, header)
	if err != nil {
		return nil, "", ErrDraftFailed
	}
	if _, err := io.WriteString(bodyWriter, draft.text); err != nil {
		_ = bodyWriter.Close()
		return nil, "", ErrDraftFailed
	}
	if err := bodyWriter.Close(); err != nil {
		return nil, "", ErrDraftFailed
	}
	return message.Bytes(), "<" + messageIDValue + ">", nil
}

func appendWasRejected(err error) bool {
	var response *imap.Error
	if errors.As(err, &response) {
		return response.Type == imap.StatusResponseTypeNo || response.Type == imap.StatusResponseTypeBad
	}
	return false
}
