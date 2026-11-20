package main

import (
	"strings"
	"testing"

	"github.com/mjl-/mox/moxvar"
)

// Usage, flag descriptions and help texts use the product name of a distribution,
// and plain mox's name without.
func TestDistributionHelp(t *testing.T) {
	help := func(words string) (usage, helpText string) {
		t.Helper()
		for _, c := range cmds {
			if strings.Join(c.words, " ") == words {
				c.gather()
				return c.makeUsage(), c.help
			}
		}
		t.Fatalf("command %q not found", words)
		return
	}

	orig := moxvar.Distribution
	defer func() { moxvar.Distribution = orig }()

	moxvar.Distribution = ""
	usage, text := help("stop")
	if !strings.Contains(usage, "usage: mox stop") || !strings.HasPrefix(text, "Shut mox down") {
		t.Fatalf("without distribution, got usage %q and help %q", usage, text)
	}

	moxvar.Distribution = "muxx-202611191730"
	usage, text = help("stop")
	if !strings.Contains(usage, "usage: muxx stop") || !strings.HasPrefix(text, "Shut muxx down") {
		t.Fatalf("with distribution, got usage %q and help %q", usage, text)
	}

	// Flag descriptions are renamed too, and references to mox itself are kept.
	usage, text = help("quickstart")
	if strings.Contains(usage, "so mox won't listen") || !strings.Contains(usage, "so muxx won't listen") {
		t.Fatalf("with distribution, flag description not renamed: %q", usage)
	}
	if strings.Contains(text, "mox quickstart") || !strings.Contains(text, "muxx quickstart") {
		t.Fatalf("with distribution, help not renamed: %q", text)
	}
	// Default values are data, e.g. from the environment, and stay as they are.
	for _, c := range cmds {
		if strings.Join(c.words, " ") == "quickstart" {
			c.gather()
			c.flag.Lookup("hostname").DefValue = "mail.mox.org"
			if usage := c.makeUsage(); !strings.Contains(usage, "mail.mox.org") {
				t.Fatalf("with distribution, flag default value renamed: %q", usage)
			}
		}
	}
}
