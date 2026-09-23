package imapclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"
)

var (
	ErrInvalidMove = errors.New("move request is invalid")
	ErrMoveFailed  = errors.New("IMAP move failed")
	dialMoveClient = imapv2client.DialTLS
)

type MoveMessagesRequest struct {
	Destination string             `json:"destination"`
	Messages    []MessageReference `json:"messages"`
}

type MoveMessagesResult struct {
	Messages []MovedMessage `json:"messages"`
}

type MovedMessage struct {
	Reference            MessageReference  `json:"reference"`
	Destination          string            `json:"destination"`
	DestinationReference *MessageReference `json:"destination_reference,omitempty"`
	Status               string            `json:"status"`
	Error                string            `json:"error"`
}

func MoveMessages(ctx context.Context, config Config, request MoveMessagesRequest) (MoveMessagesResult, error) {
	if err := validateMoveRequest(request); err != nil {
		return MoveMessagesResult{}, err
	}
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return MoveMessagesResult{}, ErrMoveFailed
	}
	if err := ctx.Err(); err != nil {
		return MoveMessagesResult{}, err
	}

	result := MoveMessagesResult{Messages: make([]MovedMessage, len(request.Messages))}
	for i, reference := range request.Messages {
		result.Messages[i] = MovedMessage{
			Reference:   reference,
			Destination: request.Destination,
			Status:      "failed",
		}
	}

	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	client, err := dialIMAPClient(ctx, dialMoveClient, address, &imapv2client.Options{
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: config.TLSServerName},
		Dialer:    &net.Dialer{Timeout: operationTimeout},
	})
	if err != nil {
		return MoveMessagesResult{}, fmt.Errorf("%w: dial", ErrMoveFailed)
	}
	defer client.Close()

	if err := loginIMAPClient(ctx, client, config.Username, config.Password); err != nil {
		return MoveMessagesResult{}, fmt.Errorf("%w: login", ErrMoveFailed)
	}
	listed, err := listMailboxData(ctx, client)
	if err != nil {
		return MoveMessagesResult{}, fmt.Errorf("%w: list destination", ErrMoveFailed)
	}
	if !hasMailbox(listed, request.Destination) {
		return MoveMessagesResult{}, ErrInvalidMove
	}

	selected, err := selectIMAPMailbox(ctx, client, request.Messages[0].Mailbox, nil)
	if err != nil {
		return MoveMessagesResult{}, fmt.Errorf("%w: select", ErrMoveFailed)
	}

	for i, reference := range request.Messages {
		if err := ctx.Err(); err != nil {
			result.Messages[i].Error = "move canceled"
			continue
		}
		if reference.UIDValidity != selected.UIDValidity {
			result.Messages[i].Error = "stale message reference"
			continue
		}

		data, err := moveIMAPMessage(ctx, client, imap.UIDSetNum(imap.UID(reference.UID)), request.Destination)
		if err != nil {
			result.Messages[i].Error = "message could not be moved"
			continue
		}
		result.Messages[i].Status = "moved"
		if destinationUID, ok := destinationUID(data); ok {
			result.Messages[i].DestinationReference = &MessageReference{
				Mailbox:     request.Destination,
				UIDValidity: data.UIDValidity,
				UID:         uint32(destinationUID),
			}
		}
	}

	return result, nil
}

func validateMoveRequest(request MoveMessagesRequest) error {
	if len(request.Messages) < 1 || len(request.Messages) > 10 {
		return ErrInvalidMove
	}
	if validateMailboxName(request.Destination) != nil {
		return ErrInvalidMove
	}
	source := ""
	for _, reference := range request.Messages {
		if validateMailboxName(reference.Mailbox) != nil || reference.UIDValidity == 0 || reference.UID == 0 {
			return ErrInvalidMove
		}
		if source == "" {
			source = reference.Mailbox
		} else if source != reference.Mailbox {
			return ErrInvalidMove
		}
	}
	if source == request.Destination || (strings.EqualFold(source, DefaultMailbox) && strings.EqualFold(request.Destination, DefaultMailbox)) {
		return ErrInvalidMove
	}
	return nil
}

func destinationUID(data *imapv2client.MoveData) (imap.UID, bool) {
	if data == nil || data.UIDValidity == 0 {
		return 0, false
	}
	set, ok := data.DestUIDs.(imap.UIDSet)
	if !ok {
		return 0, false
	}
	uids, ok := set.Nums()
	if !ok || len(uids) != 1 || uids[0] == 0 {
		return 0, false
	}
	return uids[0], true
}
