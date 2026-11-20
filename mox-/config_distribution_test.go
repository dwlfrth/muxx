package mox

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/moxvar"
)

// For builds as a distribution, the default system user and the environment
// variable for the config file use its product name.
func TestConfigDistribution(t *testing.T) {
	orig := moxvar.Distribution
	moxvar.Distribution = "muxx-202611191730"
	defer func() { moxvar.Distribution = orig }()

	log := mlog.New("mox", nil)

	// No user configured: the default is the product name. The user does not exist
	// here, so the error names it, with a hint for adding it.
	var conf Config
	conf.Static.LogLevel = "info"
	errs := PrepareStaticConfig(context.Background(), log, filepath.FromSlash("testdata/none/muxx.conf"), &conf, true, false)
	if conf.Static.User != "muxx" {
		t.Fatalf("default user: got %q, expected muxx", conf.Static.User)
	}
	var found bool
	for _, err := range errs {
		found = found || strings.Contains(err.Error(), "unknown user muxx") && strings.Contains(err.Error(), `hint: add user muxx with "useradd -d $PWD muxx"`)
	}
	if !found {
		t.Fatalf("expected error about unknown user muxx, got %v", errs)
	}

	// A missing config file gets a hint, unless MUXXCONF is set. MOXCONF does not
	// count for the distribution.
	missing := filepath.FromSlash("testdata/none/muxx.conf")
	hint := func() bool {
		t.Helper()
		_, errs := ParseConfig(context.Background(), log, missing, true, false, false)
		if len(errs) == 0 {
			t.Fatalf("expected error for missing config file")
		}
		msg := errs[0].Error()
		if strings.Contains(msg, "hint:") && !strings.Contains(msg, "hint: use muxx -config ... or set MUXXCONF=...") {
			t.Fatalf("hint does not use the product name: %q", msg)
		}
		return strings.Contains(msg, "hint:")
	}
	t.Setenv("MUXXCONF", "")
	t.Setenv("MOXCONF", "x")
	if !hint() {
		t.Fatalf("expected hint with only MOXCONF set")
	}
	t.Setenv("MUXXCONF", missing)
	if hint() {
		t.Fatalf("expected no hint with MUXXCONF set")
	}
}
