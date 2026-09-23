package imapclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
	"unicode"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
)

const defaultSearchLimit = 25

var (
	ErrInvalidSearch = errors.New("search request is invalid")
	ErrSearchFailed  = errors.New("IMAP search failed")
	dialSearchClient = imapv2client.DialTLS
)

// SearchRequest is the typed, flat search surface exposed by the MCP tool.
type SearchRequest struct {
	Mailbox          string         `json:"mailbox,omitempty"`
	Since            string         `json:"since,omitempty"`
	Before           string         `json:"before,omitempty"`
	SentSince        string         `json:"sent_since,omitempty"`
	SentBefore       string         `json:"sent_before,omitempty"`
	Headers          []HeaderFilter `json:"headers,omitempty"`
	Body             []string       `json:"body,omitempty"`
	Text             []string       `json:"text,omitempty"`
	WithFlags        []string       `json:"with_flags,omitempty"`
	WithoutFlags     []string       `json:"without_flags,omitempty"`
	LargerThanBytes  int64          `json:"larger_than_bytes,omitempty"`
	SmallerThanBytes int64          `json:"smaller_than_bytes,omitempty"`
	UIDRanges        []UIDRange     `json:"uid_ranges,omitempty"`
	Limit            int            `json:"limit,omitempty"`
}

type HeaderFilter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type UIDRange struct {
	Start uint32 `json:"start"`
	End   uint32 `json:"end"`
}

type SearchResult struct {
	Messages  []SearchMessage `json:"messages"`
	Truncated bool            `json:"truncated"`
}

type SearchMessage struct {
	Mailbox      string   `json:"mailbox"`
	UIDValidity  uint32   `json:"uid_validity"`
	UID          uint32   `json:"uid"`
	Subject      string   `json:"subject"`
	From         string   `json:"from"`
	SentAt       string   `json:"sent_at"`
	InternalDate string   `json:"internal_date"`
	Flags        []string `json:"flags"`
	SizeBytes    int64    `json:"size_bytes"`
}

func SearchMail(ctx context.Context, config Config, request SearchRequest) (SearchResult, error) {
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return SearchResult{}, ErrSearchFailed
	}
	mailbox := request.Mailbox
	if mailbox == "" {
		mailbox = DefaultMailbox
	}
	if err := validateMailboxName(mailbox); err != nil {
		return SearchResult{}, ErrInvalidSearch
	}
	criteria, limit, err := buildSearchCriteria(request)
	if err != nil {
		return SearchResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	address := net.JoinHostPort(config.Host, fmt.Sprint(config.Port))
	client, err := dialIMAPClient(ctx, dialSearchClient, address, &imapv2client.Options{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.TLSServerName},
		Dialer:    &net.Dialer{Timeout: operationTimeout},
	})
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: dial: %v", ErrSearchFailed, err)
	}
	defer client.Close()

	if err := loginIMAPClient(ctx, client, config.Username, config.Password); err != nil {
		return SearchResult{}, fmt.Errorf("%w: login: %v", ErrSearchFailed, err)
	}
	selected, err := selectIMAPMailbox(ctx, client, mailbox, &imap.SelectOptions{ReadOnly: true})
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: select: %v", ErrSearchFailed, err)
	}

	searchData, err := searchIMAP(ctx, client, &criteria, nil)
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: search: %v", ErrSearchFailed, err)
	}
	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return SearchResult{Messages: []SearchMessage{}, Truncated: false}, nil
	}

	fetched, err := fetchIMAPMessages(ctx, client, imap.UIDSetNum(uids...), &imap.FetchOptions{
		Envelope:     true,
		Flags:        true,
		InternalDate: true,
		RFC822Size:   true,
	})
	if err != nil {
		return SearchResult{}, fmt.Errorf("%w: fetch: %v", ErrSearchFailed, err)
	}

	messages := make([]SearchMessage, 0, len(fetched))
	for _, message := range fetched {
		messages = append(messages, toSearchMessage(message, mailbox, selected.UIDValidity))
	}
	sort.SliceStable(messages, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, messages[i].InternalDate)
		right, _ := time.Parse(time.RFC3339Nano, messages[j].InternalDate)
		if !left.Equal(right) {
			return left.After(right)
		}
		return messages[i].UID > messages[j].UID
	})

	truncated := len(messages) > limit
	if truncated {
		messages = messages[:limit]
	}
	return SearchResult{Messages: messages, Truncated: truncated}, nil
}

func buildSearchCriteria(request SearchRequest) (imap.SearchCriteria, int, error) {
	limit := request.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	if limit < 1 || limit > 100 {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}

	criteria := imap.SearchCriteria{}
	var err error
	criteria.Since, err = parseSearchDate(request.Since)
	if err != nil {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}
	criteria.Before, err = parseSearchDate(request.Before)
	if err != nil {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}
	criteria.SentSince, err = parseSearchDate(request.SentSince)
	if err != nil {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}
	criteria.SentBefore, err = parseSearchDate(request.SentBefore)
	if err != nil {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}
	if !criteria.Since.IsZero() && !criteria.Before.IsZero() && !criteria.Before.After(criteria.Since) {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}
	if !criteria.SentSince.IsZero() && !criteria.SentBefore.IsZero() && !criteria.SentBefore.After(criteria.SentSince) {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}

	for _, header := range request.Headers {
		if !validHeaderName(header.Name) || !validSearchText(header.Value) || strings.TrimSpace(header.Value) == "" {
			return imap.SearchCriteria{}, 0, ErrInvalidSearch
		}
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{Key: header.Name, Value: header.Value})
	}
	for _, value := range request.Body {
		if !validSearchText(value) || strings.TrimSpace(value) == "" {
			return imap.SearchCriteria{}, 0, ErrInvalidSearch
		}
		criteria.Body = append(criteria.Body, value)
	}
	for _, value := range request.Text {
		if !validSearchText(value) || strings.TrimSpace(value) == "" {
			return imap.SearchCriteria{}, 0, ErrInvalidSearch
		}
		criteria.Text = append(criteria.Text, value)
	}
	for _, value := range request.WithFlags {
		flag, ok := parseFlag(value)
		if !ok {
			return imap.SearchCriteria{}, 0, ErrInvalidSearch
		}
		criteria.Flag = append(criteria.Flag, flag)
	}
	for _, value := range request.WithoutFlags {
		flag, ok := parseFlag(value)
		if !ok {
			return imap.SearchCriteria{}, 0, ErrInvalidSearch
		}
		criteria.NotFlag = append(criteria.NotFlag, flag)
	}
	if request.LargerThanBytes < 0 || request.SmallerThanBytes < 0 {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}
	criteria.Larger = request.LargerThanBytes
	criteria.Smaller = request.SmallerThanBytes
	if criteria.Smaller > 0 && criteria.Larger > 0 && criteria.Smaller <= criteria.Larger {
		return imap.SearchCriteria{}, 0, ErrInvalidSearch
	}
	uidSet := make(imap.UIDSet, 0, len(request.UIDRanges))
	for _, uidRange := range request.UIDRanges {
		if uidRange.Start == 0 || uidRange.End == 0 || uidRange.End < uidRange.Start {
			return imap.SearchCriteria{}, 0, ErrInvalidSearch
		}
		uidSet = append(uidSet, imap.UIDRange{Start: imap.UID(uidRange.Start), Stop: imap.UID(uidRange.End)})
	}
	if len(uidSet) > 0 {
		// Keep the ranges in one UID search key; separate UID keys are ANDed.
		criteria.UID = append(criteria.UID, uidSet)
	}
	return criteria, limit, nil
}

func parseSearchDate(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return time.Time{}, errors.New("invalid date")
	}
	return parsed, nil
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < 33 || character > 126 || character == ':' || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func validSearchText(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func parseFlag(value string) (imap.Flag, bool) {
	if value == "" || strings.ContainsAny(value, " \t") || !validSearchText(value) {
		return "", false
	}
	return imap.Flag(value), true
}

func toSearchMessage(message *imapv2client.FetchMessageBuffer, mailbox string, uidValidity uint32) SearchMessage {
	result := SearchMessage{
		Mailbox:     mailbox,
		UIDValidity: uidValidity,
		UID:         uint32(message.UID),
		Flags:       []string{},
		SizeBytes:   message.RFC822Size,
	}
	for _, flag := range message.Flags {
		result.Flags = append(result.Flags, string(flag))
	}
	if !message.InternalDate.IsZero() {
		result.InternalDate = message.InternalDate.UTC().Format(time.RFC3339Nano)
	}
	if message.Envelope != nil {
		result.Subject = message.Envelope.Subject
		result.From = formatAddressList(message.Envelope.From)
		if !message.Envelope.Date.IsZero() {
			result.SentAt = message.Envelope.Date.UTC().Format(time.RFC3339Nano)
		}
	}
	return result
}

func formatAddressList(addresses []imap.Address) string {
	values := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if value := address.Addr(); value != "" {
			values = append(values, value)
		}
	}
	return strings.Join(values, ", ")
}
