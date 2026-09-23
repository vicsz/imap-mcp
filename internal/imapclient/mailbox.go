package imapclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
)

var (
	ErrInvalidMailbox = errors.New("mailbox request is invalid")
	ErrMailboxFailed  = errors.New("IMAP mailbox operation failed")
	dialListClient    = imapv2client.DialTLS
)

type Mailbox struct {
	Name       string   `json:"name"`
	Delimiter  string   `json:"delimiter"`
	Attributes []string `json:"attributes"`
}

type ListMailboxesResult struct {
	Mailboxes []Mailbox `json:"mailboxes"`
}

func ListMailboxes(ctx context.Context, config Config) (ListMailboxesResult, error) {
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return ListMailboxesResult{}, ErrMailboxFailed
	}
	if err := ctx.Err(); err != nil {
		return ListMailboxesResult{}, err
	}

	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	client, err := dialIMAPClient(ctx, dialListClient, address, &imapv2client.Options{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.TLSServerName},
		Dialer:    &net.Dialer{Timeout: operationTimeout},
	})
	if err != nil {
		return ListMailboxesResult{}, fmt.Errorf("%w: dial", ErrMailboxFailed)
	}
	defer client.Close()

	if err := loginIMAPClient(ctx, client, config.Username, config.Password); err != nil {
		return ListMailboxesResult{}, fmt.Errorf("%w: login", ErrMailboxFailed)
	}
	listed, err := listMailboxData(ctx, client)
	if err != nil {
		return ListMailboxesResult{}, fmt.Errorf("%w: list", ErrMailboxFailed)
	}

	mailboxes := make([]Mailbox, 0, len(listed))
	for _, data := range listed {
		if data == nil || data.Mailbox == "" {
			continue
		}
		attributes := make([]string, 0, len(data.Attrs))
		for _, attribute := range data.Attrs {
			attributes = append(attributes, string(attribute))
		}
		sort.Strings(attributes)
		delimiter := ""
		if data.Delim != 0 {
			delimiter = string(data.Delim)
		}
		mailboxes = append(mailboxes, Mailbox{
			Name:       data.Mailbox,
			Delimiter:  delimiter,
			Attributes: attributes,
		})
	}
	sort.SliceStable(mailboxes, func(i, j int) bool {
		return mailboxes[i].Name < mailboxes[j].Name
	})
	return ListMailboxesResult{Mailboxes: mailboxes}, nil
}

func listMailboxData(ctx context.Context, client *imapv2client.Client) ([]*imap.ListData, error) {
	return listIMAPMailboxes(ctx, client, "", "*", nil)
}

func hasMailbox(listed []*imap.ListData, name string) bool {
	for _, data := range listed {
		if data != nil && data.Mailbox == name {
			return true
		}
	}
	return false
}

func validateMailboxName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidMailbox
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return ErrInvalidMailbox
		}
	}
	return nil
}
