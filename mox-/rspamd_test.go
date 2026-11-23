package mox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjl-/mox/mlog"
)

// Test validation of the Rspamd section in mox.conf.
func TestRspamdConfig(t *testing.T) {
	domains, err := os.ReadFile(filepath.FromSlash("../testdata/smtp/domains.conf"))
	if err != nil {
		t.Fatalf("reading domains.conf: %v", err)
	}
	const base = "DataDir: data\nUser: 1000\nLogLevel: info\nHostname: mox.example\nPostmaster:\n\tAccount: mjl\n\tMailbox: postmaster\nListeners:\n\tlocal: nil\n"

	check := func(rspamd, expErr string) {
		t.Helper()
		_, errs := ParseConfig(context.Background(), mlog.New("mox", nil), writeConf(t, domains, base+rspamd), true, false, false)
		var msgs []string
		for _, err := range errs {
			msgs = append(msgs, err.Error())
		}
		s := strings.Join(msgs, "; ")
		if expErr == "" && len(errs) > 0 || expErr != "" && !strings.Contains(s, expErr) {
			t.Fatalf("rspamd config %q: got errors %q, expected %q", rspamd, s, expErr)
		}
		if strings.Contains(s, "x-secret-x") {
			t.Fatalf("error quotes the secret: %q", s)
		}
	}

	check("", "")
	check("Rspamd:\n\tURL: http://localhost:11334\n", "")
	check("Rspamd:\n\tURL: https://rspamd.example/\n\tPassword: secret\n\tEnablePassword: secret2\n\tTimeout: 30s\n", "")
	check("Rspamd:\n\tURL: ftp://localhost:11334\n", "rspamd: url must be http or https with a host")
	check("Rspamd:\n\tURL: http://\n", "rspamd: url must be http or https with a host")
	check("Rspamd:\n\tURL: http://user:secret@localhost:11334\n", "must not have credentials")
	check("Rspamd:\n\tURL: http://localhost:11334/?password=secret\n", "must not have credentials, query string")
	check("Rspamd:\n\tURL: http://localhost:11334\n\tTimeout: -1s\n", "rspamd: timeout must not be negative")
	check("Rspamd:\n\tURL: http://localhost:11334#\n", "must not have credentials, query string or fragment")
	check("Rspamd:\n\tURL: http://localhost:11334/#x\n", "must not have credentials, query string or fragment")
	check("Rspamd:\n\tURL: http://localhost:11334\n\tPassword: a\tx-secret-x\n", "rspamd: password must not contain control characters")
	check("Rspamd:\n\tURL: http://localhost:11334\n\tEnablePassword: a\x01x-secret-x\n", "rspamd: enable password must not contain control characters")
	// A url that does not parse must not be quoted in the error, it may hold a secret.
	_, errs := ParseConfig(context.Background(), mlog.New("mox", nil), writeConf(t, domains, base+"Rspamd:\n\tURL: http://user:x-secret-x@[::1\n"), true, false, false)
	for _, err := range errs {
		if strings.Contains(err.Error(), "x-secret-x") {
			t.Fatalf("error quotes the url: %v", err)
		}
	}
	if len(errs) == 0 {
		t.Fatalf("no error for unparsable url")
	}
}

func writeConf(t *testing.T, domains []byte, conf string) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "domains.conf"), domains, 0600); err != nil {
		t.Fatalf("writing domains.conf: %v", err)
	}
	p := filepath.Join(dir, "mox.conf")
	if err := os.WriteFile(p, []byte(conf), 0600); err != nil {
		t.Fatalf("writing mox.conf: %v", err)
	}
	return p
}
