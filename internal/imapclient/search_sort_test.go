package imapclient

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
	"imap-mail-mcp/internal/observability"
)

func TestSearchMailSortFastPathBoundsFetchAndPreservesOrder(t *testing.T) {
	tests := []struct {
		name          string
		sortedUIDs    []uint32
		fetchedUIDs   []uint32
		limit         int
		wantUIDs      []uint32
		wantTruncated bool
		wantFetchSet  string
	}{
		{name: "empty", sortedUIDs: []uint32{}, limit: 2, wantUIDs: []uint32{}, wantFetchSet: ""},
		{name: "exact limit", sortedUIDs: []uint32{1, 2}, fetchedUIDs: []uint32{1, 2}, limit: 2, wantUIDs: []uint32{2, 1}, wantFetchSet: "1:2"},
		{
			name: "over limit with date tie and out of order FETCH",
			// Ascending arrival order; UIDs 3 and 4 share a date. Reversing
			// preserves the established newest-first, UID-descending ordering.
			sortedUIDs:    []uint32{1, 2, 3, 4},
			fetchedUIDs:   []uint32{3, 4},
			limit:         2,
			wantUIDs:      []uint32{4, 3},
			wantTruncated: true,
			wantFetchSet:  "3:4",
		},
		{
			name:          "selected UID disappeared",
			sortedUIDs:    []uint32{1, 2, 3},
			fetchedUIDs:   []uint32{2},
			limit:         2,
			wantUIDs:      []uint32{2},
			wantTruncated: true,
			wantFetchSet:  "2:3",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := &scriptedSortFixture{capabilities: []string{"IMAP4rev1", "SORT"}, sortedUIDs: test.sortedUIDs, fetchedUIDs: test.fetchedUIDs}
			result, err := runSearchWithSortFixture(t, fixture, SearchRequest{Limit: test.limit})
			if err != nil {
				t.Fatalf("SearchMail: %v", err)
			}
			if result.Truncated != test.wantTruncated || len(result.Messages) != len(test.wantUIDs) {
				t.Fatalf("result = %#v; want UIDs %v and truncated=%t", result, test.wantUIDs, test.wantTruncated)
			}
			for index, message := range result.Messages {
				if message.UID != test.wantUIDs[index] {
					t.Fatalf("result UID order = %#v; want %v", result.Messages, test.wantUIDs)
				}
			}
			commands := fixture.commandSnapshot()
			sortCommand, fetchCommand := findCommand(commands, "UID SORT"), findCommand(commands, "UID FETCH")
			if sortCommand == "" {
				t.Fatalf("fixture did not observe UID SORT: %v", commands)
			}
			if test.wantFetchSet == "" {
				if fetchCommand != "" {
					t.Fatalf("empty result unexpectedly fetched metadata: %q", fetchCommand)
				}
			} else {
				fields := strings.Fields(fetchCommand)
				if len(fields) < 4 || fields[3] != test.wantFetchSet {
					t.Fatalf("metadata FETCH set = %q; want %q (command %q)", fieldAt(fields, 3), test.wantFetchSet, fetchCommand)
				}
			}
		})
	}
}

func TestSearchMailSortUsesTypedCriteriaAndUTF8(t *testing.T) {
	fixture := &scriptedSortFixture{
		capabilities: []string{"IMAP4rev1", "SORT"},
		sortedUIDs:   []uint32{1},
		fetchedUIDs:  []uint32{1},
	}
	_, err := runSearchWithSortFixture(t, fixture, SearchRequest{
		Limit:            1,
		Since:            "2026-01-02",
		Before:           "2026-02-03",
		SentSince:        "2026-03-04",
		SentBefore:       "2026-04-05",
		Headers:          []HeaderFilter{{Name: "X-Project", Value: "München"}},
		Body:             []string{"invoice"},
		Text:             []string{"résumé"},
		WithFlags:        []string{"\\Seen"},
		WithoutFlags:     []string{"\\Draft"},
		LargerThanBytes:  100,
		SmallerThanBytes: 1000,
		UIDRanges:        []UIDRange{{Start: 1, End: 1}, {Start: 3, End: 3}},
	})
	if err != nil {
		t.Fatalf("SearchMail: %v; commands: %v", err, fixture.commandSnapshot())
	}
	command := findCommand(fixture.commandSnapshot(), "UID SORT")
	for _, expected := range []string{
		"UID SORT (ARRIVAL) UTF-8",
		`SINCE "2-Jan-2026"`, `BEFORE "3-Feb-2026"`,
		`SENTSINCE "4-Mar-2026"`, `SENTBEFORE "5-Apr-2026"`,
		"HEADER", "X-Project", "München", "BODY", "invoice", "TEXT", "résumé",
		"SEEN", "UNDRAFT", "LARGER 100", "SMALLER 1000", "UID 1,3",
	} {
		if !strings.Contains(command, expected) {
			t.Errorf("UID SORT command %q missing %q", command, expected)
		}
	}
}

func TestSearchMailSortCapabilityAndCommandFailuresAreSanitizedAndLogged(t *testing.T) {
	tests := []struct {
		name             string
		fixture          *scriptedSortFixture
		wantOperation    string
		wantCommand      string
		wantErrorMessage string
	}{
		{
			name:             "capability discovery failure",
			fixture:          &scriptedSortFixture{capabilities: []string{"IMAP4rev1"}, capabilityFailure: true},
			wantOperation:    "capability",
			wantErrorMessage: "capability",
		},
		{
			name:             "advertised SORT command rejection",
			fixture:          &scriptedSortFixture{capabilities: []string{"IMAP4rev1", "SORT"}, sortFailure: true},
			wantOperation:    "sort",
			wantCommand:      "UID SORT",
			wantErrorMessage: "sort",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			restore := observability.SetTestWriter(&logs)
			defer restore()
			_, err := runSearchWithSortFixture(t, test.fixture, SearchRequest{
				Mailbox:   "PRIVATE-MAILBOX",
				Text:      []string{"PRIVATE-SEARCH-TERM"},
				UIDRanges: []UIDRange{{Start: 777, End: 777}},
				Limit:     1,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErrorMessage) || strings.Contains(err.Error(), "DISTINCTIVE-SERVER-DETAIL") {
				t.Fatalf("SearchMail error = %v; want sanitized %q failure", err, test.wantErrorMessage)
			}
			for _, sensitive := range []string{"DISTINCTIVE-SERVER-DETAIL", "PRIVATE-MAILBOX", "PRIVATE-SEARCH-TERM", "777"} {
				if strings.Contains(err.Error(), sensitive) || strings.Contains(logs.String(), sensitive) {
					t.Fatalf("failure output leaked %q; error=%v logs=%s", sensitive, err, logs.String())
				}
			}
			if !strings.Contains(logs.String(), "operation="+test.wantOperation) {
				t.Fatalf("operation was not safely logged: %s", logs.String())
			}
			commands := test.fixture.commandSnapshot()
			if test.wantCommand != "" && findCommand(commands, test.wantCommand) == "" {
				t.Fatalf("expected %s command, got %v", test.wantCommand, commands)
			}
			if test.wantCommand == "UID SORT" && findCommand(commands, "UID SEARCH") != "" {
				t.Fatalf("SORT failure unexpectedly fell back to SEARCH: %v", commands)
			}
		})
	}
}

func TestHasIMAPSortCapability(t *testing.T) {
	tests := []struct {
		name string
		caps imap.CapSet
		want bool
	}{
		{name: "base SORT", caps: imap.CapSet{imap.CapSort: {}}, want: true},
		{name: "SORT DISPLAY", caps: imap.CapSet{imap.Cap("SORT=DISPLAY"): {}}, want: true},
		{name: "ESORT alone", caps: imap.CapSet{imap.CapESort: {}}, want: false},
		{name: "unsupported", caps: imap.CapSet{imap.CapIMAP4rev1: {}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasIMAPSortCapability(test.caps); got != test.want {
				t.Fatalf("hasIMAPSortCapability = %t, want %t", got, test.want)
			}
		})
	}
}

func TestSearchMailFallsBackWhenSORTIsNotAdvertised(t *testing.T) {
	fixture := &scriptedSortFixture{capabilities: []string{"IMAP4rev1"}}
	_, err := runSearchWithSortFixture(t, fixture, SearchRequest{Limit: 1})
	if err != nil {
		t.Fatalf("SearchMail fallback: %v", err)
	}
	commands := fixture.commandSnapshot()
	if findCommand(commands, "UID SORT") != "" || findCommand(commands, "UID SEARCH") == "" {
		t.Fatalf("capability fallback commands = %v", commands)
	}
}

func runSearchWithSortFixture(t *testing.T, fixture *scriptedSortFixture, request SearchRequest) (SearchResult, error) {
	t.Helper()
	previous := dialSearchClient
	dialSearchClient = func(_ string, _ *imapv2client.Options) (*imapv2client.Client, error) {
		clientConn, serverConn := net.Pipe()
		go fixture.serve(serverConn)
		return imapv2client.New(clientConn, nil), nil
	}
	defer func() { dialSearchClient = previous }()
	return SearchMail(context.Background(), Config{Username: "test-user", Password: "test-password"}, request)
}

type scriptedSortFixture struct {
	mu                sync.Mutex
	capabilities      []string
	sortedUIDs        []uint32
	fetchedUIDs       []uint32
	commands          []string
	capabilityFailure bool
	sortFailure       bool
}

func (fixture *scriptedSortFixture) serve(connection net.Conn) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	write := func(response string) bool {
		_, err := io.WriteString(connection, response)
		return err == nil
	}
	if !write("* OK scripted IMAP server ready\r\n") {
		return
	}
	for {
		line, err := readScriptedIMAPCommand(reader, write)
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		fixture.mu.Lock()
		fixture.commands = append(fixture.commands, line)
		fixture.mu.Unlock()
		tag, commandIndex := fields[0], 1
		command := strings.ToUpper(fields[commandIndex])
		if command == "UID" && len(fields) > 2 {
			command = "UID " + strings.ToUpper(fields[2])
		}
		switch command {
		case "LOGIN":
			if !write(tag + " OK LOGIN completed\r\n") {
				return
			}
		case "CAPABILITY":
			if fixture.capabilityFailure {
				if !write(tag + " BAD DISTINCTIVE-SERVER-DETAIL capability unavailable\r\n") {
					return
				}
				continue
			}
			if !write("* CAPABILITY " + strings.Join(fixture.capabilities, " ") + "\r\n" + tag + " OK CAPABILITY completed\r\n") {
				return
			}
		case "EXAMINE", "SELECT":
			if !write("* 5 EXISTS\r\n* OK [UIDVALIDITY 444] UIDs valid\r\n* OK [UIDNEXT 20] next UID\r\n" + tag + " OK [READ-ONLY] selected\r\n") {
				return
			}
		case "UID SORT":
			if fixture.sortFailure {
				if !write(tag + " NO DISTINCTIVE-SERVER-DETAIL sort rejected\r\n") {
					return
				}
				continue
			}
			numbers := make([]string, 0, len(fixture.sortedUIDs))
			for _, uid := range fixture.sortedUIDs {
				numbers = append(numbers, strconv.FormatUint(uint64(uid), 10))
			}
			response := "* SORT"
			if len(numbers) > 0 {
				response += " " + strings.Join(numbers, " ")
			}
			if !write(response + "\r\n" + tag + " OK SORT completed\r\n") {
				return
			}
		case "UID FETCH":
			for _, uid := range fixture.fetchedUIDs {
				date := "02-Jan-2026 10:00:00 +0000"
				if uid >= 3 {
					date = "03-Jan-2026 10:00:00 +0000"
				}
				response := fmt.Sprintf("* %d FETCH (UID %d FLAGS () INTERNALDATE %q RFC822.SIZE 120 ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL NIL))\r\n", uid, uid, date)
				if !write(response) {
					return
				}
			}
			if !write(tag + " OK FETCH completed\r\n") {
				return
			}
		case "UID SEARCH":
			if !write("* SEARCH\r\n" + tag + " OK SEARCH completed\r\n") {
				return
			}
		case "LOGOUT":
			_ = write("* BYE logging out\r\n" + tag + " OK LOGOUT completed\r\n")
			return
		default:
			_ = write(tag + " BAD unsupported scripted command\r\n")
		}
	}
}

func readScriptedIMAPCommand(reader *bufio.Reader, write func(string) bool) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	var command strings.Builder
	command.WriteString(strings.TrimRight(line, "\r\n"))
	for {
		current := strings.TrimRight(line, "\r\n")
		closeBrace := strings.LastIndex(current, "}")
		openBrace := strings.LastIndex(current, "{")
		if closeBrace < 0 || openBrace < 0 || closeBrace != len(current)-1 {
			return command.String(), nil
		}
		length, err := strconv.Atoi(current[openBrace+1 : closeBrace])
		if err != nil || length < 0 {
			return command.String(), nil
		}
		if !write("+ continue\r\n") {
			return "", io.ErrUnexpectedEOF
		}
		literal := make([]byte, length)
		if _, err := io.ReadFull(reader, literal); err != nil {
			return "", err
		}
		command.Write(literal)
		line, err = reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		command.WriteString(strings.TrimRight(line, "\r\n"))
	}
}

func (fixture *scriptedSortFixture) commandSnapshot() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string(nil), fixture.commands...)
}

func findCommand(commands []string, prefix string) string {
	for _, command := range commands {
		fields := strings.Fields(command)
		if len(fields) < 2 {
			continue
		}
		candidate := strings.ToUpper(fields[1])
		if candidate == "UID" && len(fields) > 2 {
			candidate += " " + strings.ToUpper(fields[2])
		}
		if candidate == prefix {
			return command
		}
	}
	return ""
}

func fieldAt(fields []string, index int) string {
	if index < 0 || index >= len(fields) {
		return "<missing>"
	}
	return fields[index]
}
