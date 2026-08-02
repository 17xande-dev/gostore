// Command hashpw turns a password into the bcrypt hash that goes in
// ADMIN_PASSWORD_HASH.
//
// The password is read from stdin, never from a command-line argument, so it
// does not end up in shell history or in another user's `ps` output:
//
//	read -rs ADMIN_PASSWORD && printf %s "$ADMIN_PASSWORD" | go run ./cmd/hashpw
//
// It also prints a fresh SESSION_SECRET, since the two are always needed
// together and generating 32 random bytes by hand is a step people skip.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/17xande-dev/gostore/internal/auth"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	password, err := readPassword(os.Stdin)
	if err != nil {
		return err
	}

	hash, err := auth.HashPassword(password, auth.HashCost)
	if err != nil {
		return err
	}

	secret := make([]byte, auth.MinSecretLen)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("hashpw: generate session secret: %w", err)
	}

	fmt.Printf("ADMIN_PASSWORD_HASH=%s\n", hash)
	fmt.Printf("SESSION_SECRET=%s\n", base64.StdEncoding.EncodeToString(secret))
	return nil
}

// readPassword takes everything up to the first newline, so a piped
// `printf %s` and an interactively typed line both work.
func readPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("hashpw: read password: %w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("hashpw: no password on stdin; pipe one in, e.g. " +
			`read -rs P && printf %s "$P" | go run ./cmd/hashpw`)
	}
	return password, nil
}
