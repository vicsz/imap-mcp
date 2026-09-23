package imapclient

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	imap "github.com/emersion/go-imap/v2"
	imapv2client "github.com/emersion/go-imap/v2/imapclient"

	"imap-mail-mcp/internal/observability"
)

const operationTimeout = 15 * time.Second

type dialTLSFunc func(context.Context, string, *tls.Config) (net.Conn, error)

type client struct {
	dial    dialTLSFunc
	timeout time.Duration
}

func dialIMAPClient(ctx context.Context, dial func(string, *imapv2client.Options) (*imapv2client.Client, error), address string, options *imapv2client.Options) (*imapv2client.Client, error) {
	return observability.Measure(ctx, "connect", func() (*imapv2client.Client, error) {
		return dial(address, options)
	})
}

func loginIMAPClient(ctx context.Context, client *imapv2client.Client, username, password string) error {
	return observability.MeasureError(ctx, "login", func() error {
		return client.Login(username, password).Wait()
	})
}

func listIMAPMailboxes(ctx context.Context, client *imapv2client.Client, reference, pattern string, options *imap.ListOptions) ([]*imap.ListData, error) {
	return observability.Measure(ctx, "list", func() ([]*imap.ListData, error) {
		return client.List(reference, pattern, options).Collect()
	})
}

func selectIMAPMailbox(ctx context.Context, client *imapv2client.Client, mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	return observability.Measure(ctx, "select", func() (*imap.SelectData, error) {
		return client.Select(mailbox, options).Wait()
	})
}

func fetchIMAPMessages(ctx context.Context, client *imapv2client.Client, set imap.NumSet, options *imap.FetchOptions) ([]*imapv2client.FetchMessageBuffer, error) {
	return observability.Measure(ctx, "fetch", func() ([]*imapv2client.FetchMessageBuffer, error) {
		return client.Fetch(set, options).Collect()
	})
}

func searchIMAP(ctx context.Context, client *imapv2client.Client, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	return observability.Measure(ctx, "search", func() (*imap.SearchData, error) {
		return client.UIDSearch(criteria, options).Wait()
	})
}

func moveIMAPMessage(ctx context.Context, client *imapv2client.Client, set imap.NumSet, destination string) (*imapv2client.MoveData, error) {
	return observability.Measure(ctx, "move", func() (*imapv2client.MoveData, error) {
		return client.Move(set, destination).Wait()
	})
}

func CheckConnection(ctx context.Context, config Config) ([]string, error) {
	capabilities, err := defaultClient().checkConnection(ctx, config)
	if err != nil {
		return nil, ErrConnectionFailed
	}
	return capabilities, nil
}

func defaultClient() client {
	return client{
		dial: func(ctx context.Context, address string, tlsConfig *tls.Config) (net.Conn, error) {
			return (&tls.Dialer{NetDialer: &net.Dialer{}, Config: tlsConfig}).DialContext(ctx, "tcp", address)
		},
		timeout: operationTimeout,
	}
}

func (c client) CheckConnection(ctx context.Context, config Config) ([]string, error) {
	capabilities, err := c.checkConnection(ctx, config)
	if err != nil {
		return nil, ErrConnectionFailed
	}
	return capabilities, nil
}

func (c client) checkConnection(ctx context.Context, config Config) ([]string, error) {
	config = config.withDefaults()
	if err := validateConfig(config); err != nil {
		return nil, errors.New("invalid IMAP configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	connection, err := observability.Measure(ctx, "connect", func() (net.Conn, error) {
		return c.dial(ctx, net.JoinHostPort(config.Host, strconv.Itoa(config.Port)), &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: config.TLSServerName,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("TLS connection: %w", err)
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set connection deadline: %w", err)
	}

	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	if err := observability.MeasureError(ctx, "greeting", func() error { return expectGreeting(reader) }); err != nil {
		return nil, fmt.Errorf("read server greeting: %w", err)
	}
	if err := observability.MeasureError(ctx, "login", func() error {
		return runCommand(reader, writer, "A1", "LOGIN "+quote(config.Username)+" "+quote(config.Password), nil)
	}); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}

	var capabilities []string
	if err := observability.MeasureError(ctx, "capability", func() error {
		return runCommand(reader, writer, "A2", "CAPABILITY", func(line string) error {
			if parsed, ok := parseCapability(line); ok {
				capabilities = parsed
			}
			return nil
		})
	}); err != nil {
		return nil, fmt.Errorf("capability: %w", err)
	}
	if err := observability.MeasureError(ctx, "logout", func() error { return runCommand(reader, writer, "A3", "LOGOUT", nil) }); err != nil {
		return nil, fmt.Errorf("logout: %w", err)
	}
	return capabilities, nil
}

func expectGreeting(reader *bufio.Reader) error {
	line, err := readLine(reader)
	if err != nil || !strings.HasPrefix(strings.ToUpper(line), "* OK") {
		return errors.New("invalid greeting")
	}
	return nil
}

func runCommand(reader *bufio.Reader, writer *bufio.Writer, tag, command string, onUntagged func(string) error) error {
	if _, err := writer.WriteString(tag + " " + command + "\r\n"); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	for {
		line, err := readLine(reader)
		if err != nil {
			return err
		}
		upper := strings.ToUpper(line)
		if strings.HasPrefix(upper, tag+" OK") {
			return nil
		}
		if strings.HasPrefix(upper, tag+" NO") || strings.HasPrefix(upper, tag+" BAD") {
			return fmt.Errorf("IMAP command rejected: %s", line)
		}
		if strings.HasPrefix(line, "*") && onUntagged != nil {
			if err := onUntagged(line); err != nil {
				return err
			}
		}
	}
}

func readLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("malformed IMAP response")
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

func parseCapability(line string) ([]string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 || fields[0] != "*" || strings.ToUpper(fields[1]) != "CAPABILITY" {
		return nil, false
	}
	return fields[2:], true
}

func quote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
