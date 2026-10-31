package hosted

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

const (
	// EmailTokenBytes is the raw entropy of a verification or reset token.
	EmailTokenBytes = 32
)

// Message is one outbound account email.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Mailer delivers account email. A host without a mailer simply reports that
// email recovery is unavailable; nothing else about an account changes.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// SMTPConfig configures the built-in SMTP mailer.
type SMTPConfig struct {
	// Addr is host:port. Port 465 normally uses ImplicitTLS.
	Addr string
	// Username and Password enable SMTP AUTH when Username is non-empty.
	Username string
	Password string
	// From is the envelope and header sender.
	From string
	// FromName is the optional display name.
	FromName string
	// ImplicitTLS dials TLS immediately (SMTPS). Otherwise STARTTLS is used
	// when the server offers it.
	ImplicitTLS bool
	// Timeout bounds one delivery. Zero uses 30 seconds.
	Timeout time.Duration
}

// SMTPMailer sends mail through an SMTP relay.
type SMTPMailer struct {
	config SMTPConfig
	from   *mail.Address
}

// NewSMTPMailer validates an SMTP configuration.
func NewSMTPMailer(config SMTPConfig) (*SMTPMailer, error) {
	config.Addr = strings.TrimSpace(config.Addr)
	config.From = strings.TrimSpace(config.From)
	config.FromName = strings.TrimSpace(config.FromName)
	if config.Addr == "" {
		return nil, errors.New("smtp address is required")
	}
	if _, _, err := net.SplitHostPort(config.Addr); err != nil {
		return nil, fmt.Errorf("smtp address: %w", err)
	}
	if config.From == "" {
		return nil, errors.New("smtp sender is required")
	}
	address, err := mail.ParseAddress(config.From)
	if err != nil {
		return nil, fmt.Errorf("smtp sender: %w", err)
	}
	if config.FromName != "" {
		address = &mail.Address{Name: config.FromName, Address: address.Address}
	}
	if config.Timeout <= 0 {
		config.Timeout = 30 * time.Second
	}
	return &SMTPMailer{config: config, from: address}, nil
}

// Send delivers one plain-text message.
func (m *SMTPMailer) Send(ctx context.Context, msg Message) error {
	if m == nil {
		return errors.New("smtp mailer is not configured")
	}
	to, err := mail.ParseAddress(strings.TrimSpace(msg.To))
	if err != nil {
		return errors.New("invalid recipient address")
	}
	body, err := m.buildMessage(to, msg)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(m.config.Timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	var conn net.Conn
	if m.config.ImplicitTLS {
		dialer := &tls.Dialer{Config: &tls.Config{ServerName: hostOnly(m.config.Addr)}}
		conn, err = dialer.DialContext(ctx, "tcp", m.config.Addr)
	} else {
		dialer := &net.Dialer{Deadline: deadline}
		conn, err = dialer.DialContext(ctx, "tcp", m.config.Addr)
	}
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	client, err := smtp.NewClient(conn, hostOnly(m.config.Addr))
	if err != nil {
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer client.Close()
	if !m.config.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: hostOnly(m.config.Addr)}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	if m.config.Username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("smtp server does not offer authentication")
		}
		if err := client.Auth(smtp.PlainAuth("", m.config.Username, m.config.Password, hostOnly(m.config.Addr))); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(m.from.Address); err != nil {
		return fmt.Errorf("smtp sender: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("smtp recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(body); err != nil {
		writer.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp finish: %w", err)
	}
	return client.Quit()
}

func (m *SMTPMailer) buildMessage(to *mail.Address, msg Message) ([]byte, error) {
	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		return nil, errors.New("email subject is required")
	}
	if strings.ContainsAny(subject, "\r\n") {
		return nil, errors.New("email subject contains a newline")
	}
	var body bytes.Buffer
	encoder := quotedprintable.NewWriter(&body)
	if _, err := encoder.Write([]byte(msg.Text)); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "From: %s\r\n", m.from.String())
	fmt.Fprintf(&out, "To: %s\r\n", to.String())
	fmt.Fprintf(&out, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&out, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	out.WriteString("MIME-Version: 1.0\r\n")
	out.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	out.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	out.WriteString("\r\n")
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// MemoryMailer records messages for tests and local development.
type MemoryMailer struct {
	mu       sync.Mutex
	messages []Message
}

// Send records one message.
func (m *MemoryMailer) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

// Messages returns a copy of everything sent.
func (m *MemoryMailer) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.messages...)
}

// Len returns the number of messages sent.
func (m *MemoryMailer) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

// Last returns the most recent message.
func (m *MemoryMailer) Last() (Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.messages) == 0 {
		return Message{}, false
	}
	return m.messages[len(m.messages)-1], true
}

// NewEmailToken returns a URL-safe bearer token and the SHA-256 hash stored by
// the host.
func NewEmailToken() (token string, hash []byte, err error) {
	raw := make([]byte, EmailTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate email token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

// HashEmailToken validates a token's shape and returns its storage hash.
func HashEmailToken(token string) ([]byte, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 128 {
		return nil, ErrEmailTokenInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != EmailTokenBytes {
		return nil, ErrEmailTokenInvalid
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], nil
}
