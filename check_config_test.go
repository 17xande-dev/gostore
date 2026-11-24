package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestCheckConfigHelperProcess is not a test of its own: it is the child the
// test below starts, so that `-check-config` runs through the real run() path
// (flag parsing, config.Load, the "config: ok" line) rather than a copy of it.
func TestCheckConfigHelperProcess(t *testing.T) {
	if os.Getenv("GOSTORE_CHECK_CONFIG_CHILD") != "1" {
		t.Skip("only runs as the child of TestCheckConfigRefusesABaseURLTheServerWouldRefuse")
	}
	os.Args = []string{"gostore", "-check-config"}
	if err := run(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}

// The documented deploy order is `-check-config && -migrate && exec`. A BASE_URL
// the CSRF library refuses must therefore fail the first step, not the server
// after it has migrated.
func TestCheckConfigRefusesABaseURLTheServerWouldRefuse(t *testing.T) {
	run := func(t *testing.T, base string) (string, error) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestCheckConfigHelperProcess$")
		cmd.Env = append(os.Environ(),
			"GOSTORE_CHECK_CONFIG_CHILD=1",
			"BASE_URL="+base,
			"DATABASE_URL=postgres://u:p@localhost:5432/gostore",
			"EMAIL_QUEUE_KEY="+strings.Repeat("ab", 32),
			"PAYFAST_MERCHANT_ID=10000100",
			"PAYFAST_MERCHANT_KEY=46f0cd694581a",
			"IMAGE_DIR="+t.TempDir(),
			"SMTP_HOST=localhost",
			"EMAIL_FROM=orders@example.com",
		)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}

	if out, err := run(t, "https://store.example"); err != nil || !strings.Contains(out, "config: ok") {
		t.Fatalf("a valid BASE_URL: err=%v, output %q", err, out)
	}
	for _, base := range []string{
		"https://Store.Example",      // upper case
		"https://store.example/shop", // path
		"https://store.example:443",  // default port
		"https://user@store.example", // userinfo
		"store.example",              // no scheme
	} {
		out, err := run(t, base)
		if err == nil || strings.Contains(out, "config: ok") || !strings.Contains(out, "BASE_URL") {
			t.Errorf("-check-config with BASE_URL %q: err=%v, output %q; want a failure naming BASE_URL", base, err, out)
		}
	}
}
