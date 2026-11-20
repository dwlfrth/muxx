package moxvar

import (
	"testing"
)

// setDistribution sets Distribution for the duration of a test.
func setDistribution(t *testing.T, dist string) {
	t.Helper()
	orig := Distribution
	Distribution = dist
	t.Cleanup(func() { Distribution = orig })
}

func TestProductNames(t *testing.T) {
	type names struct {
		product, title, header, config, env, docker string
	}
	test := func(dist string, exp names) {
		t.Helper()
		setDistribution(t, dist)
		got := names{ProductName(), ProductTitle(), HeaderPrefix(), ConfigFileName(), EnvName("CONF"), EnvName("_DOCKER")}
		if got != exp {
			t.Fatalf("distribution %q: got %#v, expected %#v", dist, got, exp)
		}
	}
	test("", names{"mox", "Mox", "X-Mox-", "mox.conf", "MOXCONF", "MOX_DOCKER"})
	test("muxx-202611191730", names{"muxx", "Muxx", "X-Muxx-", "muxx.conf", "MUXXCONF", "MUXX_DOCKER"})
	// Without release part, the whole value is the product name.
	test("muxx", names{"muxx", "Muxx", "X-Muxx-", "muxx.conf", "MUXXCONF", "MUXX_DOCKER"})
	// Without product name, plain mox, instead of a crash on the empty name.
	test("-202611191730", names{"mox", "Mox", "X-Mox-", "mox.conf", "MOXCONF", "MOX_DOCKER"})
	if got := Rename("mox"); got != "mox" {
		t.Fatalf("distribution without product name: Rename changed text: %q", got)
	}
}

func TestVersionAndUserAgent(t *testing.T) {
	const base = "v0.0.18-0.20261119000000-0123456789ab-go1.27.4"

	if got := withDistribution("", base); got != base {
		t.Fatalf("without distribution: got %q, expected %q", got, base)
	}
	if got, exp := withDistribution("muxx-202611191730", base), "muxx 202611191730 (mox "+base+")"; got != exp {
		t.Fatalf("got %q, expected %q", got, exp)
	}
	if got, exp := withDistribution("muxx", base), "muxx (mox "+base+")"; got != exp {
		t.Fatalf("got %q, expected %q", got, exp)
	}
	if got := withDistribution("-202611191730", base); got != base {
		t.Fatalf("without product name: got %q, expected %q", got, base)
	}

	origVersion, origBase := Version, baseVersion
	t.Cleanup(func() { Version, baseVersion = origVersion, origBase })
	baseVersion = base

	Version = base
	setDistribution(t, "")
	if got, exp := UserAgent(), "mox/"+base; got != exp {
		t.Fatalf("user agent without distribution: got %q, expected %q", got, exp)
	}
	Distribution = "muxx-202611191730"
	Version = withDistribution(Distribution, base)
	if got, exp := UserAgent(), "muxx/202611191730 (mox "+base+")"; got != exp {
		t.Fatalf("user agent with distribution: got %q, expected %q", got, exp)
	}
}

func TestRename(t *testing.T) {
	setDistribution(t, "")
	const text = "Shut mox down, edit mox.conf or set MOXCONF."
	if got := Rename(text); got != text {
		t.Fatalf("without distribution, text changed: %q", got)
	}

	setDistribution(t, "muxx-202611191730")
	cases := []struct{ in, exp string }{
		{"Shut mox down.", "Shut muxx down."},
		{"Mox is open source.", "Muxx is open source."},
		{"MOX", "MUXX"},
		{"edit mox.conf, see mox.service and mox.freebsd.rc", "edit muxx.conf, see muxx.service and muxx.freebsd.rc"},
		{"set MOXCONF or MOX_DOCKER", "set MUXXCONF or MUXX_DOCKER"},
		{"cd /home/mox and run ./mox serve", "cd /home/muxx and run ./muxx serve"},
		{`run "mox config describe-domains"`, `run "muxx config describe-domains"`},
		// References to mox itself stay.
		{"see https://github.com/mjl-/mox and github.com/mjl-/mox/webapi", "see https://github.com/mjl-/mox and github.com/mjl-/mox/webapi"},
		{"redirect mox.example to www.mox.example", "redirect mox.example to www.mox.example"},
		{"log in as mox@localhost", "log in as mox@localhost"},
		{"package mox-/config", "package mox-/config"},
		// Only whole words.
		{"updates.xmox.nl, moxmoxmox, moxvar, MOX_SOCKETS, MOX_FILES", "updates.xmox.nl, moxmoxmox, moxvar, MOX_SOCKETS, MOX_FILES"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Rename(c.in); got != c.exp {
			t.Errorf("Rename(%q): got %q, expected %q", c.in, got, c.exp)
		}
	}
}

// Only comment lines of a configuration file are renamed, values are data and stay.
func TestRenameComments(t *testing.T) {
	setDistribution(t, "muxx-202611191730")
	const in = "# User to switch to. Default: mox.\nUser: mox\nHostname: mail.mox.org\n\t\t# Directory for mox data.\n\t\tDataDir: /var/lib/mox\n"
	const exp = "# User to switch to. Default: muxx.\nUser: mox\nHostname: mail.mox.org\n\t\t# Directory for muxx data.\n\t\tDataDir: /var/lib/mox\n"
	if got := RenameComments(in); got != exp {
		t.Fatalf("got %q, expected %q", got, exp)
	}
}

// Only lower case product names of letters and digits, with an optional release,
// are valid: others would make invalid header names.
func TestCheckDistribution(t *testing.T) {
	for _, dist := range []string{"", "muxx", "muxx-202611191730", "mux2-1.2"} {
		if err := checkDistribution(dist); err != nil {
			t.Errorf("checkDistribution(%q): %v, expected valid", dist, err)
		}
	}
	for _, dist := range []string{"-1", "Muxx-1", "mu xx-1", "über-1", "muxx-1\n", "muxx-", "muxx--1", "2muxx-1", "muxx-1 2"} {
		if err := checkDistribution(dist); err == nil {
			t.Errorf("checkDistribution(%q): no error, expected invalid", dist)
		}
	}
}

// The title of pages starts with the product title, other titles stay.
func TestRenameTitle(t *testing.T) {
	const page = "<html><head><title>Mox Webmail</title></head><body>Mox</body></html>"
	setDistribution(t, "")
	if got := string(RenameTitle([]byte(page))); got != page {
		t.Fatalf("without distribution, page changed: %q", got)
	}
	setDistribution(t, "muxx-202611191730")
	if got, exp := string(RenameTitle([]byte(page))), "<html><head><title>Muxx Webmail</title></head><body>Mox</body></html>"; got != exp {
		t.Fatalf("got %q, expected %q", got, exp)
	}
	if got := string(RenameTitle([]byte("<title>Message</title>"))); got != "<title>Message</title>" {
		t.Fatalf("other title changed: %q", got)
	}
}
