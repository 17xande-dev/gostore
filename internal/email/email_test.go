package email

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestParseTLSPolicy(t *testing.T) {
	good := map[string]TLSPolicy{
		"":          TLSStartTLS, // the default, correct for port 587
		"starttls":  TLSStartTLS,
		"STARTTLS":  TLSStartTLS,
		" starttls": TLSStartTLS,
		"tls":       TLSImplicit,
		"none":      TLSNone,
	}
	for in, want := range good {
		got, err := ParseTLSPolicy(in)
		if err != nil || got != want {
			t.Errorf("ParseTLSPolicy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	// An unrecognised value is an error rather than a fallback. Silently
	// downgrading somebody's TLS because they wrote "ssl" would be the worst of
	// the available behaviours.
	for _, in := range []string{"ssl", "yes", "true", "opportunistic", "off"} {
		if got, err := ParseTLSPolicy(in); err == nil {
			t.Errorf("ParseTLSPolicy(%q) = %q with no error; want a refusal", in, got)
		}
	}
	// And the error names the valid values, because that is what the person
	// reading it needs.
	_, err := ParseTLSPolicy("ssl")
	for _, want := range []string{"starttls", "tls", "none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
}

func TestNewSMTPSender_ValidatesConfiguration(t *testing.T) {
	valid := SMTPConfig{Host: "localhost", Port: 1025, From: "shop@example.com", TLS: TLSNone}
	if _, err := NewSMTPSender(valid); err != nil {
		t.Fatalf("a valid configuration was rejected: %v", err)
	}

	// The port and the TLS policy default rather than failing, since both have one
	// right answer for an ordinary relay.
	s, err := NewSMTPSender(SMTPConfig{Host: "localhost", Port: 587, From: "shop@example.com"})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	if s.cfg.TLS != TLSStartTLS {
		t.Errorf("TLS = %q, want it to default to starttls", s.cfg.TLS)
	}
	if s.cfg.Timeout <= 0 {
		t.Error("Timeout was not defaulted, so a hung relay would block forever")
	}

	cases := map[string]SMTPConfig{
		"no host":      {Port: 587, From: "shop@example.com"},
		"no from":      {Host: "localhost", Port: 587},
		"port zero":    {Host: "localhost", From: "shop@example.com"},
		"port too big": {Host: "localhost", Port: 70000, From: "shop@example.com"},
		"bad TLS":      {Host: "localhost", Port: 587, From: "shop@example.com", TLS: "ssl"},
	}
	for name, cfg := range cases {
		if _, err := NewSMTPSender(cfg); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestMessage_Validate(t *testing.T) {
	ok := Message{To: "a@example.com", Subject: "Receipt", Text: "Thank you"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid message was rejected: %v", err)
	}

	// A template that produced nothing is caught here rather than as a puzzling
	// rejection from a relay. The HTML part is the only optional one.
	cases := map[string]Message{
		"no recipient": {Subject: "Receipt", Text: "Thank you"},
		"no subject":   {To: "a@example.com", Text: "Thank you"},
		"no text":      {To: "a@example.com", Subject: "Receipt", HTML: "<p>Thank you</p>"},
	}
	for name, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// An invalid message never reaches the network.
	s, err := NewSMTPSender(SMTPConfig{Host: "127.0.0.1", Port: 1, From: "shop@example.com", TLS: TLSNone})
	if err != nil {
		t.Fatalf("NewSMTPSender: %v", err)
	}
	// Port 1 would fail to connect, so an error mentioning the body proves the
	// check happened before the dial.
	err = s.Send(t.Context(), Message{To: "a@example.com", Subject: "Receipt"})
	if err == nil || !strings.Contains(err.Error(), "plain-text") {
		t.Errorf("Send error = %v, want the validation failure rather than a dial error", err)
	}
}

func TestFake_CapturesMessages(t *testing.T) {
	f := NewFake()

	for _, to := range []string{"a@example.com", "b@example.com", "a@example.com"} {
		if err := f.Send(t.Context(), Message{To: to, Subject: "s", Text: "t"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	if got := len(f.Sent()); got != 3 {
		t.Errorf("Sent() = %d messages, want 3", got)
	}
	if got := len(f.To("a@example.com")); got != 2 {
		t.Errorf("To(a@example.com) = %d, want 2", got)
	}
	if got := len(f.To("nobody@example.com")); got != 0 {
		t.Errorf("To(nobody) = %d, want 0", got)
	}

	// Sent returns a copy, so a caller cannot edit the record it is reading.
	sent := f.Sent()
	sent[0].To = "tampered"
	if f.Sent()[0].To == "tampered" {
		t.Error("Sent() handed out the fake's own slice")
	}

	f.Err = errors.New("relay refused")
	if err := f.Send(t.Context(), Message{To: "c@example.com", Subject: "s", Text: "t"}); err == nil {
		t.Error("Send succeeded with Err set")
	}
	if got := len(f.Sent()); got != 3 {
		t.Errorf("a failed send was recorded: %d messages", got)
	}
}

func TestDiscard_ReportsSuccess(t *testing.T) {
	// Reporting success is deliberate: an error would make every paid order log a
	// delivery failure for a store that has simply not configured SMTP, burying
	// the failures that matter. The startup warning is where that is noticed.
	d := Discard{Log: slog.New(slog.DiscardHandler)}
	if err := d.Send(t.Context(), Message{To: "a@example.com", Subject: "s", Text: "t"}); err != nil {
		t.Errorf("Discard.Send = %v, want nil", err)
	}
	// And a nil logger does not panic, since Discard is a zero-value-usable type.
	if err := (Discard{}).Send(t.Context(), Message{To: "a@example.com"}); err != nil {
		t.Errorf("Discard{}.Send = %v, want nil", err)
	}
}
