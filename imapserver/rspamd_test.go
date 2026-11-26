package imapserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mox-"
)

// Test which IMAP changes train rspamd: those that make a message spam or ham for
// the account's junk filter, by $Junk/$NotJunk set explicitly or by moves and
// copies to mailboxes that set them. Not uploads.
func TestRspamdTraining(t *testing.T) {
	learned := make(chan string, 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		learned <- r.URL.Path
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	tc := start(t, false)
	defer tc.close()
	tc.login("mjl@mox.example", password0)

	expect := func(paths ...string) {
		t.Helper()
		for _, exp := range paths {
			select {
			case p := <-learned:
				if p != exp {
					t.Fatalf("learned %s, expected %s", p, exp)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("nothing learned, expected %s", exp)
			}
		}
		select {
		case p := <-learned:
			t.Fatalf("unexpected training %s", p)
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Without rspamd configured, nothing is sent.
	tc.client.Select("inbox")
	tc.client.Append("inbox", makeAppend(exampleMsg)) // uid 1
	tc.transactf("ok", "uid copy 1 Junk")
	expect()

	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: srv.URL}
	defer func() { mox.Conf.Static.Rspamd = nil }()

	tc.client.Append("inbox", makeAppend(exampleMsg)) // uid 2
	tc.client.Append("inbox", makeAppend(exampleMsg)) // uid 3
	tc.client.Append("inbox", makeAppend(exampleMsg)) // uid 4
	tc.client.Append("Junk", makeAppend(exampleMsg))  // Upload into Junk, Junk uid 2.
	expect()

	tc.transactf("ok", "uid move 2 Junk") // Junk uid 3.
	expect("/learnspam")
	tc.transactf("ok", "uid copy 3 Junk") // Junk uid 4.
	expect("/learnspam")

	// Without automatic junk flags, a message moved out of Junk keeps $Junk, and
	// stays spam.
	tc.client.Select("Junk")
	tc.transactf("ok", "uid move 3 Trash")
	expect()
	tc.transactf("ok", "uid move 4 Archive") // Archive uid 1.
	expect()

	// With automatic junk flags, a mailbox that is not junk or neutral makes it ham,
	// Trash included.
	// The server reads the account config concurrently: change it under its lock.
	setAccount := func(fn func(acc *config.Account)) {
		defer mox.Conf.DynamicLockUnlock()()
		acc := mox.Conf.Dynamic.Accounts["mjl"]
		fn(&acc)
		mox.Conf.Dynamic.Accounts["mjl"] = acc
	}
	setAccount(func(acc *config.Account) {
		acc.AutomaticJunkFlags.Enabled = true
		acc.JunkMailbox = regexp.MustCompile("^(junk|spam)")
		acc.NeutralMailbox = regexp.MustCompile("^(inbox|neutral|postmaster|dmarc|tlsrpt|rejects)")
	})
	defer setAccount(func(acc *config.Account) {
		acc.AutomaticJunkFlags.Enabled = false
		acc.JunkMailbox, acc.NeutralMailbox = nil, nil
	})
	tc.client.Select("Archive")
	tc.transactf("ok", "uid move 1 Trash") // Trash uid 2.
	expect("/learnham")
	// Taken out of Junk to a neutral mailbox, Inbox: not junk.
	tc.client.Select("Trash")
	tc.transactf("ok", "uid move 2 Junk") // Junk uid 5.
	expect("/learnspam")
	tc.client.Select("Junk")
	tc.transactf("ok", "uid move 5 Inbox")
	expect("/learnham")

	tc.client.Select("inbox")
	tc.transactf("ok", `uid store 4 +flags ($Junk)`)
	expect("/learnspam")
	// With both $Junk and $NotJunk, nothing is learned, as for the junk filter; once
	// $Junk is cleared, ham.
	tc.transactf("ok", `uid store 4 +flags ($NotJunk)`)
	expect()
	tc.transactf("ok", `uid store 4 -flags ($Junk)`)
	expect("/learnham")
	tc.transactf("ok", `uid store 4 +flags (\Flagged)`)
	expect()
}
