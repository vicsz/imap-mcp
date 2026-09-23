package imapclient

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func TestBuildSearchCriteriaMapsTypedFilters(t *testing.T) {
	criteria, limit, err := buildSearchCriteria(SearchRequest{
		Since:            "2026-01-02",
		Before:           "2026-02-03",
		SentSince:        "2026-03-04",
		SentBefore:       "2026-04-05",
		Headers:          []HeaderFilter{{Name: "X-Project", Value: "imap"}},
		Body:             []string{"invoice"},
		Text:             []string{"Apple"},
		WithFlags:        []string{"\\Seen"},
		WithoutFlags:     []string{"\\Draft"},
		LargerThanBytes:  100,
		SmallerThanBytes: 1000,
		UIDRanges:        []UIDRange{{Start: 10, End: 12}, {Start: 15, End: 20}},
	})
	if err != nil {
		t.Fatalf("buildSearchCriteria: %v", err)
	}
	if limit != defaultSearchLimit {
		t.Fatalf("limit = %d, want %d", limit, defaultSearchLimit)
	}
	if criteria.Since.IsZero() || criteria.Before.IsZero() || criteria.SentSince.IsZero() || criteria.SentBefore.IsZero() {
		t.Fatal("date criteria were not populated")
	}
	if len(criteria.Header) != 1 || len(criteria.Body) != 1 || len(criteria.Text) != 1 || len(criteria.Flag) != 1 || len(criteria.NotFlag) != 1 || len(criteria.UID) != 1 {
		t.Fatalf("criteria did not map all filters: %+v", criteria)
	}
	if len(criteria.UID[0]) != 2 {
		t.Fatalf("UID ranges were not combined into one set: %#v", criteria.UID)
	}
	if criteria.Larger != 100 || criteria.Smaller != 1000 {
		t.Fatalf("size criteria = %d/%d", criteria.Larger, criteria.Smaller)
	}
}

func TestBuildSearchCriteriaRejectsInvalidInput(t *testing.T) {
	cases := []SearchRequest{
		{Since: "2026-2-03"},
		{Before: "2026-01-01", Since: "2026-02-01"},
		{Limit: -1},
		{Limit: 101},
		{Headers: []HeaderFilter{{Name: "Bad Header", Value: "x"}}},
		{Headers: []HeaderFilter{{Name: "X-Test", Value: "bad\nvalue"}}},
		{Headers: []HeaderFilter{{Name: "X-Test", Value: "bad\x01value"}}},
		{Body: []string{""}},
		{Text: []string{"bad\x00value"}},
		{Text: []string{"bad\x01value"}},
		{WithFlags: []string{"\\Seen bad"}},
		{WithFlags: []string{"\\Seen\x01"}},
		{LargerThanBytes: -1},
		{SmallerThanBytes: 10, LargerThanBytes: 10},
		{UIDRanges: []UIDRange{{Start: 0, End: 4}}},
		{UIDRanges: []UIDRange{{Start: 5, End: 4}}},
	}
	for _, request := range cases {
		if _, _, err := buildSearchCriteria(request); err != ErrInvalidSearch {
			t.Errorf("request %#v returned %v, want ErrInvalidSearch", request, err)
		}
	}
}

func TestSearchMailFakeServerCoversTypedFiltersAndOrdering(t *testing.T) {
	server, user, listener := newSearchTestServer(t)
	defer server.Close()
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	appendSearchMessage(t, user, "2026-01-02T10:00:00Z", nil, "Apple old", "apple@example.com", "old body", 0)
	appendSearchMessage(t, user, "2026-02-02T10:00:00Z", nil, "Home Depot receipt", "receipts@example.com", "order body", 0)
	appendSearchMessage(t, user, "2026-03-02T10:00:00Z", []imap.Flag{imap.FlagSeen}, "GitHub update", "notifications@github.com", "pull request body", 0)
	appendSearchMessage(t, user, "2026-03-02T10:00:00Z", []imap.Flag{imap.FlagFlagged}, "Same day later UID", "other@example.com", "special text", 0)

	previous := dialSearchClient
	dialSearchClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		listener.connections <- serverConn
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialSearchClient = previous }()

	config := Config{Username: "test-user", Password: "test-password"}
	ctx := context.Background()
	tests := []struct {
		name    string
		request SearchRequest
		want    int
	}{
		{name: "unread", request: SearchRequest{WithoutFlags: []string{"\\Seen"}}, want: 3},
		{name: "body", request: SearchRequest{Body: []string{"pull request"}}, want: 1},
		{name: "text", request: SearchRequest{Text: []string{"special text"}}, want: 1},
		{name: "header", request: SearchRequest{Headers: []HeaderFilter{{Name: "From", Value: "notifications@github.com"}}}, want: 1},
		{name: "date", request: SearchRequest{Since: "2026-03-01", Before: "2026-04-01"}, want: 2},
		{name: "flag", request: SearchRequest{WithFlags: []string{"\\Flagged"}}, want: 1},
		{name: "size", request: SearchRequest{LargerThanBytes: 210}, want: 3},
		{name: "uid", request: SearchRequest{UIDRanges: []UIDRange{{Start: 2, End: 2}}}, want: 1},
		{name: "combined", request: SearchRequest{Since: "2026-03-01", Text: []string{"special"}, WithFlags: []string{"\\Flagged"}}, want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := SearchMail(ctx, config, test.request)
			if err != nil {
				t.Fatalf("SearchMail: %v", err)
			}
			if len(result.Messages) != test.want {
				t.Fatalf("message count = %d, want %d: %#v", len(result.Messages), test.want, result.Messages)
			}
			for _, message := range result.Messages {
				if message.Mailbox != "INBOX" || message.UIDValidity == 0 || message.UID == 0 {
					t.Fatalf("invalid message reference: %#v", message)
				}
			}
		})
	}
	uidRangeTests := []struct {
		name     string
		request  SearchRequest
		wantUIDs []uint32
	}{
		{
			name:     "disjoint singleton ranges",
			request:  SearchRequest{UIDRanges: []UIDRange{{Start: 1, End: 1}, {Start: 3, End: 3}}},
			wantUIDs: []uint32{1, 3},
		},
		{
			name:     "nonadjacent spans",
			request:  SearchRequest{UIDRanges: []UIDRange{{Start: 1, End: 2}, {Start: 4, End: 4}}},
			wantUIDs: []uint32{1, 2, 4},
		},
		{
			name:     "absent UID is ignored",
			request:  SearchRequest{UIDRanges: []UIDRange{{Start: 1, End: 1}, {Start: 99, End: 99}}},
			wantUIDs: []uint32{1},
		},
		{
			name:     "overlapping ranges do not duplicate results",
			request:  SearchRequest{UIDRanges: []UIDRange{{Start: 1, End: 3}, {Start: 2, End: 4}}},
			wantUIDs: []uint32{1, 2, 3, 4},
		},
		{
			name: "union is ANDed with other filters",
			request: SearchRequest{
				UIDRanges: []UIDRange{{Start: 1, End: 1}, {Start: 4, End: 4}},
				Text:      []string{"special"},
			},
			wantUIDs: []uint32{4},
		},
		{
			name:     "no UID matches",
			request:  SearchRequest{UIDRanges: []UIDRange{{Start: 99, End: 99}, {Start: 100, End: 100}}},
			wantUIDs: []uint32{},
		},
	}
	for _, test := range uidRangeTests {
		t.Run("uid ranges/"+test.name, func(t *testing.T) {
			result, err := SearchMail(ctx, config, test.request)
			if err != nil {
				t.Fatalf("SearchMail: %v", err)
			}
			if len(result.Messages) != len(test.wantUIDs) {
				t.Fatalf("message count = %d, want %d: %#v", len(result.Messages), len(test.wantUIDs), result.Messages)
			}
			wantUIDs := make(map[uint32]bool, len(test.wantUIDs))
			for _, uid := range test.wantUIDs {
				wantUIDs[uid] = true
			}
			for _, message := range result.Messages {
				if !wantUIDs[message.UID] {
					t.Fatalf("unexpected or duplicate UID %d in result %#v", message.UID, result.Messages)
				}
				delete(wantUIDs, message.UID)
			}
			if len(wantUIDs) != 0 {
				t.Fatalf("missing UIDs from result: %#v", wantUIDs)
			}
			if len(result.Messages) == 0 && result.Truncated {
				t.Fatal("empty result must not be marked truncated")
			}
		})
	}

	result, err := SearchMail(ctx, config, SearchRequest{Limit: 2})
	if err != nil {
		t.Fatalf("ordered SearchMail: %v", err)
	}
	if !result.Truncated || len(result.Messages) != 2 {
		t.Fatalf("truncated result = %#v", result)
	}
	if result.Messages[0].InternalDate < result.Messages[1].InternalDate || (result.Messages[0].InternalDate == result.Messages[1].InternalDate && result.Messages[0].UID < result.Messages[1].UID) {
		t.Fatalf("results are not newest-first: %#v", result.Messages)
	}
	withoutSeen, err := SearchMail(ctx, config, SearchRequest{WithoutFlags: []string{"\\Seen"}})
	if err != nil {
		t.Fatalf("repeat unread SearchMail: %v", err)
	}
	if len(withoutSeen.Messages) != 3 {
		t.Fatalf("metadata fetch changed flags: got %d unread messages, want 3", len(withoutSeen.Messages))
	}
}

type searchTestLiteral struct {
	*bytes.Reader
	size int64
}

func (literal searchTestLiteral) Size() int64 { return literal.size }

func newSearchTestServer(t *testing.T) (*imapserver.Server, *imapmemserver.User, *searchTestListener) {
	t.Helper()
	user := imapmemserver.NewUser("test-user", "test-password")
	mem := imapmemserver.New()
	mem.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		InsecureAuth: true,
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), &imapserver.GreetingData{}, nil
		},
	})
	listener := &searchTestListener{connections: make(chan net.Conn), closed: make(chan struct{})}
	go func() { _ = server.Serve(listener) }()
	return server, user, listener
}

func appendSearchMessage(t *testing.T, user *imapmemserver.User, date string, flags []imap.Flag, subject, from, body string, _ int) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, date)
	if err != nil {
		t.Fatalf("parse message date: %v", err)
	}
	raw := "From: " + from + "\r\n" +
		"To: recipient@example.com\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + parsed.Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: <" + strings.ReplaceAll(subject, " ", "-") + "@example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" + body
	data := []byte(raw)
	_, err = user.Append("INBOX", searchTestLiteral{Reader: bytes.NewReader(data), size: int64(len(data))}, &imap.AppendOptions{Flags: flags, Time: parsed})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
}

type searchTestListener struct {
	connections chan net.Conn
	closed      chan struct{}
}

func (listener *searchTestListener) Accept() (net.Conn, error) {
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *searchTestListener) Close() error {
	select {
	case <-listener.closed:
	default:
		close(listener.closed)
	}
	return nil
}

func (listener *searchTestListener) Addr() net.Addr { return searchTestAddr{} }

type searchTestAddr struct{}

func (searchTestAddr) Network() string { return "pipe" }
func (searchTestAddr) String() string  { return "pipe" }
