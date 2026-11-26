package store

import (
	"regexp"
	"testing"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mox-"
)

// Which flag changes train rspamd, and as what: as for the account's junk filter.
func TestRspamdLearn(t *testing.T) {
	m := Message{ID: 1}

	if _, ok := RspamdLearnFlags(m, Flags{}, Flags{Junk: true}); ok {
		t.Fatalf("training without rspamd configured")
	}
	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: "http://localhost:11334"}
	defer func() { mox.Conf.Static.Rspamd = nil }()

	flags := func(oflags, nflags Flags, expLearn, expSpam bool) {
		t.Helper()
		l, ok := RspamdLearnFlags(m, oflags, nflags)
		if ok != expLearn || ok && l.Spam != expSpam {
			t.Fatalf("flags %v to %v: learn %v spam %v, expected learn %v spam %v", oflags, nflags, ok, l.Spam, expLearn, expSpam)
		}
	}
	flags(Flags{}, Flags{Junk: true}, true, true)
	flags(Flags{}, Flags{Notjunk: true}, true, false)
	flags(Flags{Notjunk: true}, Flags{Junk: true}, true, true)
	flags(Flags{Junk: true}, Flags{Junk: true}, false, false)
	// As the account's junk filter: both set is not trained, so adding $NotJunk to a
	// message with $Junk trains nothing, and clearing $Junk then trains ham.
	flags(Flags{}, Flags{Junk: true, Notjunk: true}, false, false)
	flags(Flags{Junk: true}, Flags{Junk: true, Notjunk: true}, false, false)
	flags(Flags{Junk: true, Notjunk: true}, Flags{Notjunk: true}, true, false)
	flags(Flags{Junk: true}, Flags{}, false, false)
}

// A message taken out of Junk to a neutral mailbox is nonjunk with rspamd, not
// for Trash, and without rspamd as mox.
func TestRspamdJunkFlagsForMailboxMove(t *testing.T) {
	conf := config.Account{
		AutomaticJunkFlags: config.AutomaticJunkFlags{Enabled: true},
		JunkMailbox:        regexp.MustCompile("^(junk|spam)"),
		NeutralMailbox:     regexp.MustCompile("^(inbox|neutral|postmaster|dmarc|tlsrpt|rejects|trash)"),
	}
	inbox := Mailbox{Name: "Inbox"}
	junk := Mailbox{Name: "Junk", SpecialUse: SpecialUse{Junk: true}}
	trash := Mailbox{Name: "Trash", SpecialUse: SpecialUse{Trash: true}}
	archive := Mailbox{Name: "Archive"}

	move := func(src, dst Mailbox, expJunk, expNotjunk bool) {
		t.Helper()
		m := Message{Flags: Flags{Junk: src.Junk}}
		m.RspamdJunkFlagsForMailboxMove(src, dst, conf)
		if m.Junk != expJunk || m.Notjunk != expNotjunk {
			t.Fatalf("move %s to %s: junk %v notjunk %v, expected %v %v", src.Name, dst.Name, m.Junk, m.Notjunk, expJunk, expNotjunk)
		}
	}
	// Without rspamd: as mox, neutral clears.
	move(junk, inbox, false, false)

	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: "http://localhost:11334"}
	defer func() { mox.Conf.Static.Rspamd = nil }()
	move(junk, inbox, false, true)
	move(junk, trash, false, false)
	move(junk, archive, false, true)
	move(inbox, junk, true, false)
	move(archive, inbox, false, false)
}
