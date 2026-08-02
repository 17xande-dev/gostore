package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var testSecret = bytes.Repeat([]byte("k"), MinSecretLen)

func TestSession_RoundTrips(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	value := IssueSession(testSecret, time.Hour, now)
	expiry, err := VerifySession(testSecret, value, now)
	if err != nil {
		t.Fatalf("VerifySession: %v", err)
	}
	if want := now.Add(time.Hour); !expiry.Equal(want) {
		t.Errorf("expiry = %s, want %s", expiry, want)
	}

	// Still valid a minute before it runs out, gone a second after.
	if _, err := VerifySession(testSecret, value, now.Add(59*time.Minute)); err != nil {
		t.Errorf("VerifySession before expiry: %v", err)
	}
	if _, err := VerifySession(testSecret, value, now.Add(time.Hour+time.Second)); !errors.Is(err, ErrExpired) {
		t.Errorf("VerifySession after expiry = %v, want ErrExpired", err)
	}
}

func TestVerifySession_RejectsForgeries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	value := IssueSession(testSecret, time.Hour, now)
	payload, mac, _ := strings.Cut(value, ".")

	// Extending the expiry is the attack that matters: the payload is readable
	// and editable by anyone holding the cookie, so only the signature stops it.
	farFuture := base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(now.Add(100*24*time.Hour).Unix(), 10)))

	cases := map[string]string{
		"empty":              "",
		"no separator":       payload + mac,
		"payload rewritten":  farFuture + "." + mac,
		"signature dropped":  payload + ".",
		"signature garbage":  payload + "." + base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"not base64":         "!!!.???",
		"payload not a time": base64.RawURLEncoding.EncodeToString([]byte("soon")) + "." + mac,
	}
	for name, bad := range cases {
		if _, err := VerifySession(testSecret, bad, now); err == nil {
			t.Errorf("%s: VerifySession accepted %q", name, bad)
		}
	}

	// A value signed with a different secret — another deployment's cookie, or
	// a rotated secret — must not verify here.
	other := IssueSession(bytes.Repeat([]byte("j"), MinSecretLen), time.Hour, now)
	if _, err := VerifySession(testSecret, other, now); err == nil {
		t.Error("a session signed with another secret was accepted")
	}
	if _, err := VerifySession(testSecret, value, now); err != nil {
		t.Errorf("the genuine value stopped verifying: %v", err)
	}
}

func TestVerifySession_ExpiryIsSignedNotJustCookieMetadata(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	// An already-expired session reports ErrExpired rather than a signature
	// failure, so a handler can tell "signed out" from "tampered with".
	value := IssueSession(testSecret, -time.Minute, now)
	if _, err := VerifySession(testSecret, value, now); !errors.Is(err, ErrExpired) {
		t.Errorf("VerifySession = %v, want ErrExpired", err)
	}
}

func TestPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("the hash contains the password")
	}

	if !CheckPassword(hash, "correct horse battery staple") {
		t.Error("the correct password was rejected")
	}
	for _, wrong := range []string{"", "correct horse battery stapl", "Correct Horse Battery Staple"} {
		if CheckPassword(hash, wrong) {
			t.Errorf("password %q was accepted", wrong)
		}
	}

	// A missing or malformed ADMIN_PASSWORD_HASH must fail closed, not open.
	for _, badHash := range []string{"", "not-a-bcrypt-hash", "$2a$10$tooshort"} {
		if CheckPassword(badHash, "anything") {
			t.Errorf("hash %q accepted a password", badHash)
		}
	}

	if _, err := HashPassword("", bcrypt.MinCost); err == nil {
		t.Error("HashPassword accepted an empty password")
	}
}

func TestHashPassword_IsSalted(t *testing.T) {
	a, err := HashPassword("same", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("same", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not doing its job")
	}
}
