package email

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wneessen/go-mail"
)

// TLSPolicy is how the connection to the mail server is secured.
type TLSPolicy string

const (
	// TLSStartTLS upgrades a plain connection and requires the upgrade to
	// succeed. This is the default and the right answer for port 587.
	TLSStartTLS TLSPolicy = "starttls"
	// TLSImplicit dials TLS directly, which is what port 465 expects.
	TLSImplicit TLSPolicy = "tls"
	// TLSNone sends credentials and order details in the clear. It exists for
	// mailpit in development and is logged loudly; it is never right against a
	// mail server that is not on the same machine.
	TLSNone TLSPolicy = "none"
)

// ParseTLSPolicy converts a configured string, naming the valid values on
// failure rather than falling back to a default — silently downgrading
// somebody's TLS because they wrote "ssl" would be the worst of the options.
func ParseTLSPolicy(s string) (TLSPolicy, error) {
	switch p := TLSPolicy(strings.ToLower(strings.TrimSpace(s))); p {
	case TLSStartTLS, TLSImplicit, TLSNone:
		return p, nil
	case "":
		return TLSStartTLS, nil
	default:
		return "", fmt.Errorf("email: TLS policy must be one of %s, %s, %s; got %q",
			TLSStartTLS, TLSImplicit, TLSNone, s)
	}
}

// SMTPConfig is what SMTPSender needs. Username and Password may be empty, for a
// relay on a private network that authenticates by address — mailpit in
// development being the obvious case.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string

	// From is the envelope and header sender. A mail server will usually reject
	// a From it does not consider itself responsible for, so this has to be an
	// address on a domain the relay accepts.
	From string
	// ReplyTo is optional, and is where a customer's reply should land when that
	// is not the From address.
	ReplyTo string

	TLS     TLSPolicy
	Timeout time.Duration
}

// SMTPSender delivers mail over SMTP.
type SMTPSender struct{ cfg SMTPConfig }

// NewSMTPSender validates the configuration and returns a Sender.
//
// It deliberately does not connect: a mail server that is down at boot is not a
// reason to refuse to start a shop, because the shop's job — taking an order and
// recording it — does not depend on mail. The first send is where a connection
// problem surfaces, and it surfaces as a logged failure against an order that is
// already safely paid.
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("email: SMTP host is required")
	}
	if strings.TrimSpace(cfg.From) == "" {
		return nil, fmt.Errorf("email: a From address is required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("email: SMTP port %d is out of range", cfg.Port)
	}
	if cfg.TLS == "" {
		cfg.TLS = TLSStartTLS
	}
	if _, err := ParseTLSPolicy(string(cfg.TLS)); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &SMTPSender{cfg: cfg}, nil
}

// Send delivers one message.
//
// A fresh client is built per send rather than one being held on the struct. A
// go-mail Client carries connection state, so sharing one would need a mutex that
// serialised every email behind whichever send was currently blocked on a slow
// server — and building one is a struct and a few options, next to nothing beside
// the TCP dial that follows it.
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	if err := m.Validate(); err != nil {
		return err
	}

	msg := mail.NewMsg()
	if err := msg.From(s.cfg.From); err != nil {
		return fmt.Errorf("email: from address %q: %w", s.cfg.From, err)
	}
	if err := msg.To(m.To); err != nil {
		return fmt.Errorf("email: recipient %q: %w", m.To, err)
	}
	if s.cfg.ReplyTo != "" {
		if err := msg.ReplyTo(s.cfg.ReplyTo); err != nil {
			return fmt.Errorf("email: reply-to %q: %w", s.cfg.ReplyTo, err)
		}
	}
	msg.Subject(m.Subject)

	// Plain text is the body and HTML is the alternative, in that order, which is
	// what makes a client that cannot or will not render HTML show the readable
	// version rather than nothing.
	msg.SetBodyString(mail.TypeTextPlain, m.Text)
	if m.HTML != "" {
		msg.AddAlternativeString(mail.TypeTextHTML, m.HTML)
	}

	client, err := s.client()
	if err != nil {
		return err
	}
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("email: send to %s: %w", m.To, err)
	}
	return nil
}

func (s *SMTPSender) client() (*mail.Client, error) {
	opts := []mail.Option{
		mail.WithPort(s.cfg.Port),
		mail.WithTimeout(s.cfg.Timeout),
	}

	switch s.cfg.TLS {
	case TLSImplicit:
		opts = append(opts, mail.WithSSL())
	case TLSNone:
		opts = append(opts, mail.WithTLSPolicy(mail.NoTLS))
	default:
		// Mandatory rather than opportunistic: a relay that offers no STARTTLS
		// should fail loudly, not quietly send an order in the clear.
		opts = append(opts, mail.WithTLSPolicy(mail.TLSMandatory))
	}

	// No credentials means no authentication at all, rather than an empty
	// PLAIN attempt that a relay would reject.
	if s.cfg.Username != "" {
		opts = append(opts,
			mail.WithSMTPAuth(mail.SMTPAuthAutoDiscover),
			mail.WithUsername(s.cfg.Username),
			mail.WithPassword(s.cfg.Password))
	}

	client, err := mail.NewClient(s.cfg.Host, opts...)
	if err != nil {
		return nil, fmt.Errorf("email: build SMTP client for %s: %w", s.cfg.Host, err)
	}
	return client, nil
}
