//go:build integration

package imapclient

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestIMAPConnectionIntegration(t *testing.T) {
	config := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	capabilities, err := CheckConnection(ctx, config)
	if err != nil {
		t.Fatalf("IMAP connection failed: %v", err)
	}
	if len(capabilities) == 0 {
		t.Fatal("IMAP server returned no capabilities")
	}
}

func TestMailboxSelectionIntegration(t *testing.T) {
	config := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := ListMailboxes(ctx, config)
	if err != nil {
		t.Fatalf("list mailboxes: %v", err)
	}
	if len(result.Mailboxes) == 0 {
		t.Fatal("list mailboxes returned no mailboxes")
	}

	for _, mailbox := range result.Mailboxes {
		if strings.EqualFold(mailbox.Name, DefaultMailbox) || containsMailboxAttribute(mailbox.Attributes, "\\Noselect") {
			continue
		}
		if _, err := SearchMail(ctx, config, SearchRequest{Mailbox: mailbox.Name, Limit: 1}); err == nil {
			return
		}
	}
	t.Skip("account did not expose a searchable non-INBOX mailbox")
}

func TestSearchInboxIntegration(t *testing.T) {
	config := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	result, err := SearchMail(ctx, config, SearchRequest{Mailbox: DefaultMailbox, Limit: 1})
	if err != nil {
		t.Fatalf("search Inbox: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("Inbox search returned no messages")
	}
}

func TestReadInboxMessageIntegration(t *testing.T) {
	config := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	search, err := SearchMail(ctx, config, SearchRequest{Mailbox: DefaultMailbox, Limit: 1})
	if err != nil {
		t.Fatalf("search Inbox: %v", err)
	}
	if len(search.Messages) == 0 {
		t.Fatal("Inbox search returned no messages to read")
	}
	reference := search.Messages[0]
	beforeFlags := append([]string(nil), reference.Flags...)

	read, err := ReadMessages(ctx, config, ReadMessagesRequest{Messages: []MessageReference{{
		Mailbox:     DefaultMailbox,
		UIDValidity: reference.UIDValidity,
		UID:         reference.UID,
	}}})
	if err != nil {
		t.Fatalf("read Inbox message: %v", err)
	}
	if len(read.Messages) != 1 || read.Messages[0].Error != "" {
		t.Fatal("read did not return one successful message")
	}

	after, err := SearchMail(ctx, config, SearchRequest{
		Mailbox:   DefaultMailbox,
		UIDRanges: []UIDRange{{Start: reference.UID, End: reference.UID}},
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("recheck Inbox message flags: %v", err)
	}
	if len(after.Messages) != 1 || !slicesEqual(beforeFlags, after.Messages[0].Flags) {
		t.Fatal("reading the message changed its flags")
	}
}

func integrationConfig(t *testing.T) Config {
	t.Helper()
	if err := LoadDotEnv("../../.env"); err != nil {
		t.Fatal("could not load project environment")
	}
	config, err := LoadConfig(os.Getenv)
	if err != nil {
		t.Fatal("IMAP configuration is missing or invalid")
	}
	return config
}

func containsMailboxAttribute(attributes []string, want string) bool {
	for _, attribute := range attributes {
		if strings.EqualFold(attribute, want) {
			return true
		}
	}
	return false
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
