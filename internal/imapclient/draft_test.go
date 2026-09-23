package imapclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	gomail "github.com/emersion/go-message/mail"
	"mime"
	"mime/quotedprintable"
)

func TestValidateCreateDraftRequest(t *testing.T) {
	validTo := []string{"Ada Lovelace <ada@example.test>"}
	validSubject := "A note"
	validReply := &MessageReference{Mailbox: "INBOX", UIDValidity: 3, UID: 1}
	cases := []CreateDraftRequest{
		{},
		{Mode: "other", Text: "body"},
		{Mode: "new", Text: "body", Subject: &validSubject},
		{Mode: "new", Text: "body", To: &validTo},
		{Mode: "new", Text: "body", To: &validTo, Subject: stringPointer(" ")},
		{Mode: "new", Text: "body", To: &validTo, Subject: stringPointer("bad\r\nsubject")},
		{Mode: "new", Text: "body", To: &[]string{"one@example.test, two@example.test"}, Subject: &validSubject},
		{Mode: "new", Text: "body", To: &[]string{"one@example.test\nbcc:x"}, Subject: &validSubject},
		{Mode: "new", Text: "body", To: &validTo, Cc: &[]string{strings.Repeat("a", 1)}, Subject: &validSubject, ReplyTo: validReply},
		{Mode: "new", Text: "", To: &validTo, Subject: &validSubject},
		{Mode: "new", Text: strings.Repeat("x", maxDraftTextBytes+1), To: &validTo, Subject: &validSubject},
		{Mode: "new", Text: "body", To: recipientPointers(21), Subject: &validSubject},
		{Mode: "reply", Text: "body"},
		{Mode: "reply", Text: "body", ReplyTo: &MessageReference{Mailbox: "INBOX", UIDValidity: 1}},
		{Mode: "reply", Text: "body", ReplyTo: validReply, Subject: &validSubject},
		{Mode: "reply", Text: "body", ReplyTo: validReply, To: &[]string{}},
	}
	for index, request := range cases {
		if _, err := validateCreateDraftRequest(request); err != ErrInvalidDraft {
			t.Errorf("case %d returned %v, want ErrInvalidDraft", index, err)
		}
	}

	tooLongSubject := strings.Repeat("é", maxDraftSubject+1)
	if _, err := validateCreateDraftRequest(CreateDraftRequest{Mode: "new", Text: "body", To: &validTo, Subject: &tooLongSubject}); err != ErrInvalidDraft {
		t.Fatalf("oversized subject returned %v, want ErrInvalidDraft", err)
	}
	valid, err := validateCreateDraftRequest(CreateDraftRequest{Mode: "new", Text: "body", To: &validTo, Subject: &validSubject})
	if err != nil || len(valid.to) != 1 || valid.subject != validSubject {
		t.Fatalf("valid new draft returned %#v, %v", valid, err)
	}
}

func TestCreateDraftNewFakeServer(t *testing.T) {
	harness := newDraftTestServer(t, draftTestOptions{})
	to := []string{"Ada Lovelace <ada@example.test>"}
	cc := []string{"cc@example.test"}
	bcc := []string{"Hidden Recipient <hidden@example.test>"}
	result, err := CreateDraft(context.Background(), testDraftConfig(), CreateDraftRequest{
		Mode: "new", To: &to, Cc: &cc, Bcc: &bcc, Subject: stringPointer("Résumé"), Text: "Hello, café.\r\nSecond line.",
	})
	if err != nil {
		t.Fatalf("CreateDraft: %v", err)
	}
	if result.Status != "created" || result.Mode != "new" || result.MessageID == "" || result.Threaded != nil {
		t.Fatalf("result = %#v", result)
	}
	if result.DraftReference == nil || result.DraftReference.Mailbox != "Drafts" || result.DraftReference.UIDValidity == 0 || result.DraftReference.UID == 0 {
		t.Fatalf("draft reference = %#v", result.DraftReference)
	}
	record := receiveDraftAppend(t, harness)
	if record.mailbox != "Drafts" || !containsIMAPFlag(record.flags, imap.FlagDraft) {
		t.Fatalf("APPEND destination/flags = %q/%v", record.mailbox, record.flags)
	}
	parsed := parseCapturedDraft(t, record.raw)
	if parsed.Header.Get("MIME-Version") != "1.0" {
		t.Fatalf("MIME-Version = %q", parsed.Header.Get("MIME-Version"))
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/plain" || params["charset"] != "utf-8" {
		t.Fatalf("Content-Type = %q, %v", parsed.Header.Get("Content-Type"), err)
	}
	if parsed.Header.Get("Bcc") == "" {
		t.Fatal("draft did not preserve the Bcc recipient")
	}
	from, err := netmail.ParseAddress(parsed.Header.Get("From"))
	if err != nil || from.Address != "victor@example.test" {
		t.Fatalf("From = %q, %v", parsed.Header.Get("From"), err)
	}
	toAddresses, err := (&netmail.AddressParser{}).ParseList(parsed.Header.Get("To"))
	if err != nil || len(toAddresses) != 1 || toAddresses[0].Address != "ada@example.test" {
		t.Fatalf("To = %q, %v", parsed.Header.Get("To"), err)
	}
	ccAddresses, err := (&netmail.AddressParser{}).ParseList(parsed.Header.Get("Cc"))
	if err != nil || len(ccAddresses) != 1 || ccAddresses[0].Address != "cc@example.test" {
		t.Fatalf("Cc = %q, %v", parsed.Header.Get("Cc"), err)
	}
	body, err := readDraftBody(parsed)
	if err != nil || body != "Hello, café.\r\nSecond line." {
		t.Fatalf("body = %q, %v", body, err)
	}
	if !strings.HasPrefix(result.MessageID, "<") || !strings.HasSuffix(result.MessageID, ">") {
		t.Fatalf("Message-ID = %q", result.MessageID)
	}
}

func TestCreateDraftRejectsInvalidConfiguredSenderBeforeConnecting(t *testing.T) {
	previousDial := dialDraftClient
	dialDraftClient = func(string, *imapv2client.Options) (*imapv2client.Client, error) {
		t.Fatal("invalid sender attempted an IMAP connection")
		return nil, nil
	}
	t.Cleanup(func() { dialDraftClient = previousDial })

	to := []string{"to@example.test"}
	_, err := CreateDraft(context.Background(), Config{
		Username: "test-user", Password: "test-password", FromAddress: "bad\r\nBcc: injected@example.test",
	}, CreateDraftRequest{Mode: "new", To: &to, Subject: stringPointer("Subject"), Text: "body"})
	if err != ErrInvalidConfiguration {
		t.Fatalf("CreateDraft error = %v, want ErrInvalidConfiguration", err)
	}
}

func TestCreateDraftReplyPrefersReplyToAndThreads(t *testing.T) {
	harness := newDraftTestServer(t, draftTestOptions{})
	appendDraftSource(t, harness.user, "From: Sender <sender@example.test>\r\nReply-To: Reply Desk <reply@example.test>\r\nSubject: =?utf-8?Q?R=C3=A9sum=C3=A9?=\r\nMessage-ID: <current@example.test>\r\nReferences: <root@example.test>\r\n\r\nprivate source body", []imap.Flag{imap.FlagFlagged})
	status, err := harness.user.Status("INBOX", &imap.StatusOptions{UIDValidity: true})
	if err != nil {
		t.Fatalf("INBOX status: %v", err)
	}
	result, err := CreateDraft(context.Background(), testDraftConfig(), CreateDraftRequest{
		Mode: "reply", ReplyTo: &MessageReference{Mailbox: "INBOX", UIDValidity: status.UIDValidity, UID: 1}, Text: "Reply text",
	})
	if err != nil {
		t.Fatalf("CreateDraft reply: %v", err)
	}
	if result.Status != "created" || result.Mode != "reply" || result.Threaded == nil || !*result.Threaded {
		t.Fatalf("result = %#v", result)
	}
	parsed := parseCapturedDraft(t, receiveDraftAppend(t, harness).raw)
	toAddresses, err := (&netmail.AddressParser{}).ParseList(parsed.Header.Get("To"))
	if err != nil || len(toAddresses) != 1 || toAddresses[0].Address != "reply@example.test" {
		t.Fatalf("reply To = %q, %v", parsed.Header.Get("To"), err)
	}
	if got := parsed.Header.Get("In-Reply-To"); got != "<current@example.test>" {
		t.Fatalf("In-Reply-To = %q", got)
	}
	if got := parsed.Header.Get("References"); got != "<root@example.test> <current@example.test>" {
		t.Fatalf("References = %q", got)
	}
	var header gomail.Header
	header = gomail.HeaderFromMap(map[string][]string(parsed.Header))
	subject, err := header.Subject()
	if err != nil || subject != "Re: Résumé" {
		t.Fatalf("Subject = %q, %v", subject, err)
	}
	if len(parsed.Header["Bcc"]) != 0 {
		t.Fatal("reply unexpectedly has Bcc")
	}
	flags, err := SearchMail(context.Background(), testDraftConfig(), SearchRequest{
		Mailbox: "INBOX", UIDRanges: []UIDRange{{Start: 1, End: 1}}, Limit: 1,
	})
	if err != nil || len(flags.Messages) != 1 || !containsString(flags.Messages[0].Flags, "\\Flagged") || containsString(flags.Messages[0].Flags, "\\Seen") {
		t.Fatalf("source flags changed: %v", err)
	}
}

func TestCreateDraftReplyFallsBackAndCanBeUnthreaded(t *testing.T) {
	harness := newDraftTestServer(t, draftTestOptions{})
	appendDraftSource(t, harness.user, "From: sender@example.test\r\nReply-To: invalid address\r\nSubject: Re: Existing\r\nReferences: <ignored@example.test>\r\n\r\nsource", nil)
	status, err := harness.user.Status("INBOX", &imap.StatusOptions{UIDValidity: true})
	if err != nil {
		t.Fatalf("INBOX status: %v", err)
	}
	result, err := CreateDraft(context.Background(), testDraftConfig(), CreateDraftRequest{
		Mode: "reply", ReplyTo: &MessageReference{Mailbox: "INBOX", UIDValidity: status.UIDValidity, UID: 1}, Text: "Reply",
	})
	if err != nil {
		t.Fatalf("CreateDraft reply: %v", err)
	}
	if result.Threaded == nil || *result.Threaded {
		t.Fatalf("threaded = %#v, want false", result.Threaded)
	}
	parsed := parseCapturedDraft(t, receiveDraftAppend(t, harness).raw)
	addresses, err := (&netmail.AddressParser{}).ParseList(parsed.Header.Get("To"))
	if err != nil || len(addresses) != 1 || addresses[0].Address != "sender@example.test" {
		t.Fatalf("fallback To = %q, %v", parsed.Header.Get("To"), err)
	}
	if parsed.Header.Get("Subject") != "Re: Existing" || parsed.Header.Get("In-Reply-To") != "" || parsed.Header.Get("References") != "" {
		t.Fatalf("unthreaded reply headers: subject=%q in-reply-to=%q references=%q", parsed.Header.Get("Subject"), parsed.Header.Get("In-Reply-To"), parsed.Header.Get("References"))
	}
}

func TestCreateDraftRejectsUnavailableOrAmbiguousDraftsMailbox(t *testing.T) {
	for _, test := range []struct {
		name      string
		mailboxes []imap.ListData
	}{
		{name: "missing", mailboxes: []imap.ListData{{Mailbox: "INBOX"}}},
		{name: "ambiguous", mailboxes: []imap.ListData{{Mailbox: "INBOX"}, {Mailbox: "Drafts", Attrs: []imap.MailboxAttr{imap.MailboxAttrDrafts}}, {Mailbox: "Other Drafts", Attrs: []imap.MailboxAttr{imap.MailboxAttrDrafts}}}},
		{name: "noselect", mailboxes: []imap.ListData{{Mailbox: "INBOX"}, {Mailbox: "Drafts", Attrs: []imap.MailboxAttr{imap.MailboxAttrDrafts, imap.MailboxAttrNoSelect}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newDraftTestServer(t, draftTestOptions{mailboxes: test.mailboxes})
			to := []string{"to@example.test"}
			_, err := CreateDraft(context.Background(), testDraftConfig(), CreateDraftRequest{Mode: "new", To: &to, Subject: stringPointer("Subject"), Text: "body"})
			if err != ErrDraftFailed {
				t.Fatalf("CreateDraft error = %v, want ErrDraftFailed", err)
			}
			if len(harness.appends) != 0 {
				t.Fatal("APPEND occurred without exactly one usable Drafts mailbox")
			}
		})
	}
}

func TestCreateDraftRejectsStaleOrMissingReplySource(t *testing.T) {
	harness := newDraftTestServer(t, draftTestOptions{})
	appendDraftSource(t, harness.user, "From: sender@example.test\r\nSubject: Source\r\n\r\nbody", nil)
	status, err := harness.user.Status("INBOX", &imap.StatusOptions{UIDValidity: true})
	if err != nil {
		t.Fatalf("INBOX status: %v", err)
	}
	for _, reference := range []MessageReference{
		{Mailbox: "INBOX", UIDValidity: status.UIDValidity + 1, UID: 1},
		{Mailbox: "INBOX", UIDValidity: status.UIDValidity, UID: 999},
	} {
		_, err := CreateDraft(context.Background(), testDraftConfig(), CreateDraftRequest{Mode: "reply", ReplyTo: &reference, Text: "Reply"})
		if err != ErrDraftFailed {
			t.Fatalf("CreateDraft error = %v, want ErrDraftFailed", err)
		}
	}
	if len(harness.appends) != 0 {
		t.Fatal("APPEND occurred for an invalid source reference")
	}
}

func TestCreateDraftAppendFailureAndMissingUIDPlus(t *testing.T) {
	to := []string{"to@example.test"}
	request := CreateDraftRequest{Mode: "new", To: &to, Subject: stringPointer("Subject"), Text: "body"}

	t.Run("server rejection", func(t *testing.T) {
		harness := newDraftTestServer(t, draftTestOptions{appendErr: &imap.Error{Type: imap.StatusResponseTypeNo}})
		_, err := CreateDraft(context.Background(), testDraftConfig(), request)
		if err != ErrDraftFailed {
			t.Fatalf("CreateDraft error = %v, want ErrDraftFailed", err)
		}
		if len(harness.appends) != 1 {
			t.Fatalf("APPEND attempts = %d, want 1", len(harness.appends))
		}
	})

	t.Run("successful append without APPENDUID", func(t *testing.T) {
		harness := newDraftTestServer(t, draftTestOptions{omitAppendUID: true})
		result, err := CreateDraft(context.Background(), testDraftConfig(), request)
		if err != nil {
			t.Fatalf("CreateDraft: %v", err)
		}
		if result.Status != "created" || result.DraftReference != nil {
			t.Fatalf("result = %#v", result)
		}
		if record := receiveDraftAppend(t, harness); record.mailbox != "Drafts" {
			t.Fatalf("APPEND mailbox = %q", record.mailbox)
		}
	})
}

func TestCreateDraftCancellationAfterAppendIsUnknownAndNotRetried(t *testing.T) {
	started := make(chan struct{})
	continueAppend := make(chan struct{})
	harness := newDraftTestServer(t, draftTestOptions{appendStarted: started, appendBlock: continueAppend})
	defer close(continueAppend)
	to := []string{"to@example.test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultChannel := make(chan struct {
		result CreateDraftResult
		err    error
	}, 1)
	go func() {
		result, err := CreateDraft(ctx, testDraftConfig(), CreateDraftRequest{Mode: "new", To: &to, Subject: stringPointer("Subject"), Text: "body"})
		resultChannel <- struct {
			result CreateDraftResult
			err    error
		}{result, err}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("APPEND did not start")
	}
	cancel()
	select {
	case outcome := <-resultChannel:
		if outcome.err != nil || outcome.result.Status != "unknown" || outcome.result.MessageID == "" {
			t.Fatalf("result = %#v, error = %v", outcome.result, outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("CreateDraft did not return after cancellation")
	}
	if len(harness.appends) != 1 {
		t.Fatalf("APPEND attempts = %d, want 1", len(harness.appends))
	}
}

type draftAppendRecord struct {
	mailbox string
	raw     []byte
	flags   []imap.Flag
}

type draftTestOptions struct {
	mailboxes     []imap.ListData
	appendErr     error
	omitAppendUID bool
	appendStarted chan struct{}
	appendBlock   <-chan struct{}
}

type draftTestHarness struct {
	user    *imapmemserver.User
	appends chan draftAppendRecord
}

type draftTestSession struct {
	imapserver.SessionIMAP4rev2
	options draftTestOptions
	appends chan<- draftAppendRecord
}

func (session *draftTestSession) List(writer *imapserver.ListWriter, _ string, _ []string, options *imap.ListOptions) error {
	mailboxes := session.options.mailboxes
	if mailboxes == nil {
		mailboxes = []imap.ListData{
			{Mailbox: "INBOX"},
			{Mailbox: "Drafts", Attrs: []imap.MailboxAttr{imap.MailboxAttrDrafts}},
		}
	}
	for _, mailbox := range mailboxes {
		if options != nil && options.SelectSpecialUse && len(mailbox.Attrs) == 0 {
			continue
		}
		if options == nil || (!options.ReturnSpecialUse && !options.SelectSpecialUse) {
			mailbox.Attrs = nil
		}
		if err := writer.WriteList(&mailbox); err != nil {
			return err
		}
	}
	return nil
}

func (session *draftTestSession) Append(mailbox string, literal imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	raw, err := io.ReadAll(literal)
	if err != nil {
		return nil, err
	}
	var flags []imap.Flag
	if options != nil {
		flags = append(flags, options.Flags...)
	}
	session.appends <- draftAppendRecord{mailbox: mailbox, raw: raw, flags: flags}
	if session.options.appendStarted != nil {
		close(session.options.appendStarted)
	}
	if session.options.appendBlock != nil {
		<-session.options.appendBlock
	}
	if session.options.appendErr != nil {
		return nil, session.options.appendErr
	}
	data, err := session.SessionIMAP4rev2.Append(mailbox, searchTestLiteral{Reader: bytes.NewReader(raw), size: int64(len(raw))}, options)
	if session.options.omitAppendUID {
		return nil, err
	}
	return data, err
}

func newDraftTestServer(t *testing.T, options draftTestOptions) *draftTestHarness {
	t.Helper()
	user := imapmemserver.NewUser("test-user", "test-password")
	for _, mailbox := range []string{"INBOX", "Drafts"} {
		if err := user.Create(mailbox, nil); err != nil {
			t.Fatalf("create test mailbox: %v", err)
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(user)
	appends := make(chan draftAppendRecord, 4)
	server := imapserver.New(&imapserver.Options{
		InsecureAuth: true,
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			session, ok := mem.NewSession().(imapserver.SessionIMAP4rev2)
			if !ok {
				return nil, nil, errors.New("test session lacks IMAP4rev2")
			}
			return &draftTestSession{SessionIMAP4rev2: session, options: options, appends: appends}, &imapserver.GreetingData{}, nil
		},
	})
	listener := &searchTestListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	previousDraft := dialDraftClient
	previousSearch := dialSearchClient
	dialDraftClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	dialSearchClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	t.Cleanup(func() {
		dialDraftClient = previousDraft
		dialSearchClient = previousSearch
	})
	return &draftTestHarness{user: user, appends: appends}
}

func testDraftConfig() Config {
	return Config{Username: "test-user", Password: "test-password", FromAddress: "Victor <victor@example.test>"}
}

func appendDraftSource(t *testing.T, user *imapmemserver.User, raw string, flags []imap.Flag) {
	t.Helper()
	data := []byte(raw)
	if _, err := user.Append("INBOX", searchTestLiteral{Reader: bytes.NewReader(data), size: int64(len(data))}, &imap.AppendOptions{Flags: flags}); err != nil {
		t.Fatalf("append source message: %v", err)
	}
}

func receiveDraftAppend(t *testing.T, harness *draftTestHarness) draftAppendRecord {
	t.Helper()
	select {
	case record := <-harness.appends:
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("APPEND was not observed")
		return draftAppendRecord{}
	}
}

func parseCapturedDraft(t *testing.T, raw []byte) *netmail.Message {
	t.Helper()
	parsed, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse RFC 5322 draft: %v", err)
	}
	return parsed
}

func readDraftBody(parsed *netmail.Message) (string, error) {
	var reader io.Reader = parsed.Body
	if strings.EqualFold(parsed.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
		reader = quotedprintable.NewReader(parsed.Body)
	}
	body, err := io.ReadAll(reader)
	return string(body), err
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stringPointer(value string) *string { return &value }

func recipientPointers(count int) *[]string {
	values := make([]string, count)
	for i := range values {
		values[i] = "recipient@example.test"
	}
	return &values
}

func containsIMAPFlag(flags []imap.Flag, expected imap.Flag) bool {
	for _, flag := range flags {
		if strings.EqualFold(string(flag), string(expected)) {
			return true
		}
	}
	return false
}
