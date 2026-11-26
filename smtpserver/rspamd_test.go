package smtpserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/dns"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/rspamd"
	"github.com/mjl-/mox/smtp"
	"github.com/mjl-/mox/smtpclient"
	"github.com/mjl-/mox/store"
	"github.com/mjl-/mox/subjectpass"
)

// fakeRspamd answers /checkv3 like rspamd, with a configurable verdict.
type fakeRspamd struct {
	sync.Mutex
	status    int    // HTTP status, 200 for a verdict.
	result    string // JSON verdict.
	checks    int
	deliverTo []string
	rcpts     []string
	helos     []string
	messages  []string
}

func (f *fakeRspamd) set(status int, result string) {
	f.Lock()
	defer f.Unlock()
	f.status, f.result = status, result
}

func (f *fakeRspamd) count() int {
	f.Lock()
	defer f.Unlock()
	return f.checks
}

func (f *fakeRspamd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	if r.URL.Path != "/checkv3" {
		http.NotFound(w, r)
		return
	}
	f.checks++
	_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		if p.FormName() == "metadata" {
			var meta struct {
				DeliverTo string   `json:"deliver_to"`
				Rcpt      []string `json:"rcpt"`
				Helo      string   `json:"helo"`
			}
			json.NewDecoder(p).Decode(&meta)
			f.deliverTo = append(f.deliverTo, meta.DeliverTo)
			f.helos = append(f.helos, meta.Helo)
			f.rcpts = append(f.rcpts, meta.Rcpt...)
		} else {
			buf, _ := io.ReadAll(p)
			f.messages = append(f.messages, string(buf))
		}
	}
	if f.status != http.StatusOK {
		w.WriteHeader(f.status)
		w.Write([]byte(`{"error":"test error"}`))
		return
	}
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	pw, _ := mw.CreateFormField("result")
	pw.Write([]byte(f.result))
	mw.Close()
	w.Header().Set("Content-Type", mw.FormDataContentType())
	w.Write([]byte(buf.String()))
}

func verdict(action string, skipped bool) string {
	return fmt.Sprintf(`{"is_skipped":%v,"action":%q,"score":1.5,"required_score":15,"symbols":{"TEST_SYMBOL":{"name":"TEST_SYMBOL","score":1.5}}}`, skipped, action)
}

// Test delivery decisions with rspamd configured.
func TestRspamd(t *testing.T) {
	resolver := dns.MockResolver{
		A: map[string][]string{
			"example.org.":    {"127.0.0.10"}, // For mx check.
			"reject.example.": {"127.0.0.10"},
		},
		TXT: map[string][]string{
			"example.org.":           {"v=spf1 ip4:127.0.0.10 -all"},
			"_dmarc.example.org.":    {"v=DMARC1;p=reject"},
			"reject.example.":        {"v=spf1 ip4:127.0.0.99 -all"},
			"_dmarc.reject.example.": {"v=DMARC1;p=reject"},
		},
		PTR: map[string][]string{
			"127.0.0.10": {"example.org."},
		},
	}
	ts := newTestServer(t, filepath.FromSlash("../testdata/smtp/mox.conf"), resolver)
	defer ts.close()

	fake := &fakeRspamd{status: http.StatusOK, result: verdict("no action", false)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: srv.URL}
	defer func() { mox.Conf.Static.Rspamd = nil }()

	var msgCount int
	message := func(from string) string {
		msgCount++
		return strings.ReplaceAll(fmt.Sprintf("From: <%s>\nTo: <mjl@mox.example>\nSubject: test\nMessage-Id: <rspamd%d@example.org>\n\nrspamd test message %d\n", from, msgCount, msgCount), "\n", "\r\n")
	}
	deliverx := func(mailFrom, msgFrom, rcptTo string) error {
		var err error
		ts.run(func(client *smtpclient.Client) {
			msg := message(msgFrom)
			err = client.Deliver(ctxbg, mailFrom, rcptTo, int64(len(msg)), strings.NewReader(msg), false, false, false)
		})
		return err
	}
	deliver := func() error {
		return deliverx("remote@example.org", "remote@example.org", "mjl@mox.example")
	}
	expErr := func(err error, permanent bool, code int, secode string) {
		t.Helper()
		ts.smtpErr(err, &smtpclient.Error{Permanent: permanent, Code: code, Secode: secode})
	}

	// lastMessage returns the most recently delivered message in a mailbox.
	lastMessage := func(mailbox string) store.Message {
		t.Helper()
		mb, err := bstore.QueryDB[store.Mailbox](ctxbg, ts.acc.DB).FilterNonzero(store.Mailbox{Name: mailbox}).FilterEqual("Expunged", false).Get()
		tcheck(t, err, "get mailbox")
		m, err := bstore.QueryDB[store.Message](ctxbg, ts.acc.DB).FilterNonzero(store.Message{MailboxID: mb.ID}).FilterEqual("Expunged", false).SortDesc("ID").Limit(1).Get()
		tcheck(t, err, "get last message")
		return m
	}
	expReason := func(mailbox, text string) {
		t.Helper()
		m := lastMessage(mailbox)
		// Unfold the header lines, the reason header is wrapped.
		prefix := strings.NewReplacer("\r\n\t", "", "\r\n ", " ").Replace(string(m.MsgPrefix))
		if !strings.Contains(prefix, text) {
			t.Fatalf("message prefix does not contain %q:\n%s", text, m.MsgPrefix)
		}
	}

	setDest := func(addr string, fn func(dest *config.Destination)) {
		acc := mox.Conf.Dynamic.Accounts[ts.acc.Name]
		dest := acc.Destinations[addr]
		fn(&dest)
		acc.Destinations[addr] = dest
		accDest := mox.Conf.AccountDestinationsLocked[addr]
		accDest.Destination = dest
		mox.Conf.AccountDestinationsLocked[addr] = accDest
		mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc
	}

	// "no action" delivers to the normal mailbox, with the delivered address as user.
	tcheck(t, deliver(), "deliver")
	ts.checkCount("Inbox", 1)
	expReason("Inbox", "rspamd: action no action")
	if fake.deliverTo[len(fake.deliverTo)-1] != "mjl@mox.example" || fake.rcpts[len(fake.rcpts)-1] != "mjl@mox.example" {
		t.Fatalf("deliver_to %v, rcpt %v", fake.deliverTo, fake.rcpts)
	}
	// rspamd gets the message with our Received header for the recipient first.
	if msg := fake.messages[len(fake.messages)-1]; !strings.HasPrefix(msg, "Received: from ") || !strings.Contains(msg[:strings.Index(msg, "\r\nFrom: ")], "for <mjl@mox.example>") {
		t.Fatalf("message to rspamd does not start with our received header:\n%s", msg)
	}

	// A message to an alias is checked for each member. Once a check failed, the
	// other members are not checked again.
	n0 := fake.count()
	tcheck(t, deliverx("remote@example.org", "remote@example.org", "public@mox.example"), "deliver")
	if n := fake.count() - n0; n != 2 {
		t.Fatalf("alias with 2 members checked %d times, expected 2", n)
	}
	fake.set(http.StatusInternalServerError, "")
	n0 = fake.count()
	tcheck(t, deliverx("remote@example.org", "remote@example.org", "public@mox.example"), "deliver")
	if n := fake.count() - n0; n != 1 {
		t.Fatalf("alias with failing rspamd checked %d times, expected 1", n)
	}
	fake.set(http.StatusOK, verdict("no action", false))
	ts.checkCount("Inbox", 5) // 1 earlier, 2x2 for the alias members.

	// Once the checks for a message took the time limit, the remaining recipients get
	// the most recent result, without asking rspamd, noted in their reason header.
	// With separate recipients: copies for alias members all get the header of the
	// first member, as without rspamd.
	origLimit := rspamdChecksLimit
	rspamdChecksLimit = 0
	n0 = fake.count()
	ts.run(func(client *smtpclient.Client) {
		msg := message("remote@example.org")
		_, err := client.DeliverMultiple(ctxbg, "remote@example.org", []string{"mjl@mox.example", "móx@mox.example"}, int64(len(msg)), strings.NewReader(msg), true, true, false)
		tcheck(t, err, "deliver to two recipients")
	})
	rspamdChecksLimit = origLimit
	if n := fake.count() - n0; n != 1 {
		t.Fatalf("message to 2 recipients after time limit checked %d times, expected 1", n)
	}
	// One copy was checked, the other got its result.
	inboxMb, err := bstore.QueryDB[store.Mailbox](ctxbg, ts.acc.DB).FilterNonzero(store.Mailbox{Name: "Inbox"}).FilterEqual("Expunged", false).Get()
	tcheck(t, err, "get inbox")
	copies, err := bstore.QueryDB[store.Message](ctxbg, ts.acc.DB).FilterNonzero(store.Message{MailboxID: inboxMb.ID}).FilterEqual("Expunged", false).SortDesc("ID").Limit(2).List()
	tcheck(t, err, "list copies")
	var nreused int
	for _, m := range copies {
		prefix := strings.NewReplacer("\r\n\t", "", "\r\n ", " ").Replace(string(m.MsgPrefix))
		if strings.Contains(prefix, "rspamd: result of an earlier recipient of this message reused, time limit for rspamd checks reached: action no action") {
			nreused++
		} else if !strings.Contains(prefix, "rspamd: action no action") {
			t.Fatalf("checked copy without rspamd verdict:\n%s", m.MsgPrefix)
		}
	}
	if nreused != 1 {
		t.Fatalf("%d copies with reused result, expected 1", nreused)
	}
	ts.checkCount("Inbox", 7)

	// Junk verdicts go to the Junk mailbox, with $Junk, message unchanged.
	fake.set(http.StatusOK, verdict("add header", false))
	tcheck(t, deliver(), "deliver")
	fake.set(http.StatusOK, verdict("rewrite subject", false))
	tcheck(t, deliver(), "deliver")
	ts.checkCount("Junk", 2)
	if m := lastMessage("Junk"); !m.Junk || m.Notjunk {
		t.Fatalf("junk message flags junk %v notjunk %v", m.Junk, m.Notjunk)
	} else if buf, err := os.ReadFile(ts.acc.MessagePath(m.ID)); err != nil || !strings.Contains(string(buf), "\r\nSubject: test\r\n") {
		t.Fatalf("junk message changed: %v\n%s", err, buf)
	}

	// Rejects: permanent and temporary.
	fake.set(http.StatusOK, verdict("reject", false))
	expErr(deliver(), true, smtp.C550MailboxUnavail, smtp.SePol7DeliveryUnauth1)
	fake.set(http.StatusOK, verdict("greylist", false))
	expErr(deliver(), false, smtp.C450MailboxUnavail, smtp.SePol7Other0)
	fake.set(http.StatusOK, verdict("soft reject", false))
	expErr(deliver(), false, smtp.C450MailboxUnavail, smtp.SePol7Other0)

	// A skip with a verdict, e.g. GTUBE, is applied.
	fake.set(http.StatusOK, verdict("reject", true))
	expErr(deliver(), true, smtp.C550MailboxUnavail, smtp.SePol7DeliveryUnauth1)

	// Without a usable verdict, mox's own checks decide: these accept the message.
	for _, tc := range []struct {
		status        int
		result, check string
	}{
		{http.StatusOK, verdict("no action", true), "skipped the message without verdict"},
		{http.StatusInternalServerError, "", "rspamd: unavailable, mox's own checks decide"},
		{http.StatusNotFound, "", "rspamd 4.1.1 or later is required"},
		{http.StatusOK, verdict("bogus", false), "unknown rspamd action"},
	} {
		fake.set(tc.status, tc.result)
		n := fake.count()
		tcheck(t, deliver(), "deliver")
		if fake.count() != n+1 {
			t.Fatalf("rspamd not consulted")
		}
		expReason("Inbox", tc.check)
		expReason("Inbox", "mox's own checks decide")
		// Details of errors talking to rspamd, such as its URL, are only in the log.
		if m := lastMessage("Inbox"); strings.Contains(string(m.MsgPrefix), srv.URL) || strings.Contains(string(m.MsgPrefix), "test error") {
			t.Fatalf("reason header has rspamd error details:\n%s", m.MsgPrefix)
		}
	}
	ts.checkCount("Inbox", 11)

	// A DMARC reject policy is enforced by mox, before rspamd.
	fake.set(http.StatusOK, verdict("no action", false))
	n := fake.count()
	expErr(deliverx("remote@reject.example", "remote@reject.example", "mjl@mox.example"), true, smtp.C550MailboxUnavail, smtp.SePol7MultiAuthFails26)
	if fake.count() != n {
		t.Fatalf("rspamd consulted for message rejected by dmarc policy")
	}

	// A DMARC-validated From address the account sent to is accepted without rspamd.
	sent := store.Message{MsgFromLocalpart: "mjl", MsgFromDomain: "mox.example", Size: int64(len(deliverMessage))}
	tinsertmsg(t, ts.acc, "Sent", &sent, deliverMessage)
	for _, addr := range []string{"remote@example.org", "remote@unaligned.example"} {
		lp, dom, _ := strings.Cut(addr, "@")
		err := ts.acc.DB.Insert(ctxbg, &store.Recipient{MessageID: sent.ID, Localpart: lp, Domain: dom, OrgDomain: dom, Sent: time.Now()})
		tcheck(t, err, "insert recipient")
	}
	fake.set(http.StatusOK, verdict("reject", false))
	n = fake.count()
	tcheck(t, deliver(), "deliver")
	if fake.count() != n {
		t.Fatalf("rspamd consulted for known recipient")
	}
	expReason("Inbox", "earlier message recipient, accepted without rspamd")
	// But not if the From address is not DMARC-validated.
	expErr(deliverx("remote@example.org", "remote@unaligned.example", "mjl@mox.example"), true, smtp.C550MailboxUnavail, smtp.SePol7DeliveryUnauth1)
	if fake.count() != n+1 {
		t.Fatalf("rspamd not consulted for unvalidated known recipient")
	}
	_, err = bstore.QueryDB[store.Recipient](ctxbg, ts.acc.DB).Delete()
	tcheck(t, err, "remove recipients")

	// The user for per-user statistics is without catchall separator suffix.
	fake.set(http.StatusOK, verdict("no action", false))
	dom := mox.Conf.Dynamic.Domains["mox.example"]
	dom.LocalpartCatchallSeparatorsEffective = []string{"+"}
	mox.Conf.Dynamic.Domains["mox.example"] = dom
	tcheck(t, deliverx("remote@example.org", "remote@example.org", "mjl+tag@mox.example"), "deliver")
	if s := fake.deliverTo[len(fake.deliverTo)-1]; s != "mjl@mox.example" {
		t.Fatalf("deliver_to %q, expected mjl@mox.example", s)
	}
	if s := fake.rcpts[len(fake.rcpts)-1]; s != "mjl+tag@mox.example" {
		t.Fatalf("rcpt %q, expected mjl+tag@mox.example", s)
	}

	// A ruleset's mailbox applies; AcceptRejectsToMailbox catches permanent rejects,
	// not temporary ones.
	setDest("mjl@mox.example", func(dest *config.Destination) {
		dest.Rulesets = []config.Ruleset{{
			SMTPMailFromRegexp:         `^remote@example\.org$`,
			SMTPMailFromRegexpCompiled: regexp.MustCompile(`^remote@example\.org$`),
			Mailbox:                    "Ruleset",
			AcceptRejectsToMailbox:     "Accepted",
		}}
	})
	tcheck(t, deliver(), "deliver")
	ts.checkCount("Ruleset", 1)
	// A junk verdict goes to Junk, also with a ruleset: automatic junk flags for
	// the ruleset's mailbox could clear $Junk, or set $NotJunk and train it as ham.
	fake.set(http.StatusOK, verdict("add header", false))
	tcheck(t, deliver(), "deliver")
	ts.checkCount("Ruleset", 1)
	ts.checkCount("Junk", 3)
	if m := lastMessage("Junk"); !m.Junk || m.Notjunk {
		t.Fatalf("junk message for ruleset, flags junk %v notjunk %v", m.Junk, m.Notjunk)
	}
	expReason("Junk", "rspamd: action add header")
	fake.set(http.StatusOK, verdict("reject", false))
	tcheck(t, deliver(), "deliver")
	ts.checkCount("Accepted", 1)
	fake.set(http.StatusOK, verdict("greylist", false))
	expErr(deliver(), false, smtp.C450MailboxUnavail, smtp.SePol7Other0)
	ts.checkCount("Accepted", 1)
	setDest("mjl@mox.example", func(dest *config.Destination) { dest.Rulesets = nil })

	// Introbox: senders without reputation go there, known senders to Inbox.
	fake.set(http.StatusOK, verdict("no action", false))
	acc := mox.Conf.Dynamic.Accounts[ts.acc.Name]
	acc.Introbox = "Introbox"
	acc.AutomaticJunkFlags.Enabled = true
	acc.AutomaticJunkFlags.JunkMailboxRegexp = "^(junk|spam)"
	acc.AutomaticJunkFlags.NeutralMailboxRegexp = "^(inbox|introbox|neutral|postmaster|dmarc|tlsrpt|rejects)"
	acc.JunkMailbox = regexp.MustCompile(acc.AutomaticJunkFlags.JunkMailboxRegexp)
	acc.NeutralMailbox = regexp.MustCompile(acc.AutomaticJunkFlags.NeutralMailboxRegexp)
	mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc
	tcheck(t, deliverx("intro@example.org", "intro@example.org", "mjl@mox.example"), "deliver")
	ts.checkCount("Introbox", 1)
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{lastMessage("Introbox").ID}, "Inbox", 0, false)
	inbox := lastMessage("Inbox").ID
	tcheck(t, deliverx("intro@example.org", "intro@example.org", "mjl@mox.example"), "deliver")
	ts.checkCount("Introbox", 0)
	if lastMessage("Inbox").ID == inbox {
		t.Fatalf("known sender not delivered to inbox")
	}
	acc.Introbox = ""
	mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc

	// With automatic junk flags (enabled above), a junk verdict for a message
	// matching a ruleset with a neutral mailbox, e.g. Inbox for forwarded mail, goes
	// to Junk with $Junk: in the ruleset's mailbox the flag would be cleared.
	setDest("mjl@mox.example", func(dest *config.Destination) {
		dest.Rulesets = []config.Ruleset{{
			SMTPMailFromRegexp:         `^remote@example\.org$`,
			SMTPMailFromRegexpCompiled: regexp.MustCompile(`^remote@example\.org$`),
			Mailbox:                    "Inbox",
		}}
	})
	fake.set(http.StatusOK, verdict("add header", false))
	nInbox := lastMessage("Inbox").ID
	junkBefore := lastMessage("Junk").ID
	tcheck(t, deliver(), "deliver")
	if lastMessage("Inbox").ID != nInbox {
		t.Fatalf("junk message for ruleset delivered to inbox")
	} else if m := lastMessage("Junk"); m.ID == junkBefore || !m.Junk || m.Notjunk {
		t.Fatalf("junk message for ruleset not in junk with $Junk: %v, junk %v notjunk %v", m.ID == junkBefore, m.Junk, m.Notjunk)
	}
	setDest("mjl@mox.example", func(dest *config.Destination) { dest.Rulesets = nil })
	fake.set(http.StatusOK, verdict("no action", false))

	// changeJunk changes the mailbox named Junk: it loses the Junk role, and
	// optionally gets another name.
	changeJunk := func(newName string) {
		t.Helper()
		err := ts.acc.DB.Write(ctxbg, func(tx *bstore.Tx) error {
			mb, err := bstore.QueryTx[store.Mailbox](tx).FilterNonzero(store.Mailbox{Name: "Junk"}).FilterEqual("Expunged", false).Get()
			if err != nil {
				return err
			}
			mb.Junk = false
			if newName != "" {
				mb.Name = newName
			}
			return tx.Update(&mb)
		})
		tcheck(t, err, "changing junk mailbox")
	}
	expJunkRole := func() store.Mailbox {
		t.Helper()
		mb, err := bstore.QueryDB[store.Mailbox](ctxbg, ts.acc.DB).FilterEqual("Junk", true).FilterEqual("Expunged", false).Get()
		tcheck(t, err, "get mailbox with junk role")
		if mb.Name != "Junk" {
			t.Fatalf("mailbox with junk role is %q, expected Junk", mb.Name)
		}
		return mb
	}

	// A mailbox named Junk without the Junk role gets the role back.
	fake.set(http.StatusOK, verdict("add header", false))
	changeJunk("")
	tcheck(t, deliver(), "deliver")
	junk := expJunkRole()
	expReason("Junk", "rspamd: action add header")

	// Without a mailbox named Junk, it is created with the role.
	changeJunk("OldJunk")
	tcheck(t, deliver(), "deliver")
	if mb := expJunkRole(); mb.ID == junk.ID {
		t.Fatalf("junk mailbox not recreated")
	}
	ts.checkCount("Junk", 1)

	// The name configured for Junk in InitialMailboxes is used, not "Junk".
	mox.Conf.Static.InitialMailboxes.SpecialUse.Junk = "Spam"
	changeJunk("OldJunk2")
	err = ts.acc.DB.Write(ctxbg, func(tx *bstore.Tx) error {
		// A plain mailbox named Junk, which must not get the role.
		var modseq store.ModSeq
		_, _, err := ts.acc.MailboxEnsure(tx, "Junk", false, store.SpecialUse{}, &modseq)
		return err
	})
	tcheck(t, err, "creating plain junk mailbox")
	tcheck(t, deliver(), "deliver")
	if mb, err := bstore.QueryDB[store.Mailbox](ctxbg, ts.acc.DB).FilterEqual("Junk", true).FilterEqual("Expunged", false).Get(); err != nil || mb.Name != "Spam" {
		t.Fatalf("mailbox with junk role %q, %v, expected Spam", mb.Name, err)
	}
	ts.checkCount("Spam", 1)
	ts.checkCount("Junk", 0)
	mox.Conf.Static.InitialMailboxes.SpecialUse.Junk = ""
	// Back to a mailbox named Junk with the role, for the rest of the test.
	err = ts.acc.DB.Write(ctxbg, func(tx *bstore.Tx) error {
		for _, x := range []struct {
			name string
			junk bool
		}{{"Spam", false}, {"Junk", true}} {
			mb, err := bstore.QueryTx[store.Mailbox](tx).FilterNonzero(store.Mailbox{Name: x.name}).FilterEqual("Expunged", false).Get()
			if err != nil {
				return err
			}
			mb.Junk = x.junk
			if err := tx.Update(&mb); err != nil {
				return err
			}
		}
		return nil
	})
	tcheck(t, err, "restoring junk role")

	// With NoJunkMailboxCreate, junk goes to the normal mailbox with $Junk. With
	// automatic junk flags (enabled above), Inbox is neutral and clears $Junk, as for
	// any delivery.
	mox.Conf.Static.Rspamd.NoJunkMailboxCreate = true
	changeJunk("")
	tcheck(t, deliver(), "deliver")
	expReason("Inbox", "rspamd: action add header")
	if m := lastMessage("Inbox"); m.Junk {
		t.Fatalf("junk delivered to inbox kept $Junk despite automatic junk flags")
	}
	acc.AutomaticJunkFlags.Enabled = false
	mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc
	tcheck(t, deliver(), "deliver")
	if m := lastMessage("Inbox"); !m.Junk {
		t.Fatalf("junk delivered to inbox without $Junk")
	}
	mox.Conf.Static.Rspamd.NoJunkMailboxCreate = false

	// Messages to DMARC report addresses are processed without rspamd.
	setDest("mjl@mox.example", func(dest *config.Destination) { dest.DMARCReports = true })
	n = fake.count()
	deliver()
	if fake.count() != n {
		t.Fatalf("rspamd consulted for dmarc report address")
	}
}

// Test subjectpass with rspamd: a first-time sender with DMARC pass that rspamd
// rejects gets a subjectpass token, and a message with the token is accepted
// without asking rspamd.
func TestRspamdSubjectpass(t *testing.T) {
	resolver := dns.MockResolver{
		A: map[string][]string{
			"example.org.": {"127.0.0.10"}, // For mx check.
		},
		TXT: map[string][]string{
			"example.org.":        {"v=spf1 ip4:127.0.0.10 -all"},
			"_dmarc.example.org.": {"v=DMARC1;p=reject"},
		},
		PTR: map[string][]string{
			"127.0.0.10": {"example.org."},
		},
	}
	ts := newTestServer(t, filepath.FromSlash("../testdata/smtp/mox.conf"), resolver)
	defer ts.close()

	fake := &fakeRspamd{status: http.StatusOK, result: verdict("reject", false)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: srv.URL}
	defer func() { mox.Conf.Static.Rspamd = nil }()

	deliver := func(msg string) error {
		var err error
		ts.run(func(client *smtpclient.Client) {
			err = client.Deliver(ctxbg, "remote@example.org", "mjl@mox.example", int64(len(msg)), strings.NewReader(msg), false, false, false)
		})
		return err
	}
	expReject := func(err error, hint bool) string {
		t.Helper()
		cerr := ts.smtpErr(err, &smtpclient.Error{Permanent: true, Code: smtp.C550MailboxUnavail, Secode: smtp.SePol7DeliveryUnauth1})
		i := strings.Index(cerr.Line, subjectpass.Explanation)
		if hint != (i >= 0) {
			t.Fatalf("error line %q, expected subjectpass hint %v", cerr.Line, hint)
		}
		if i < 0 {
			return ""
		}
		return cerr.Line[i+len(subjectpass.Explanation):]
	}

	// Without subjectpass configured, no hint.
	expReject(deliver(deliverMessage), false)

	acc := mox.Conf.Dynamic.Accounts[ts.acc.Name]
	acc.SubjectPass.Period = time.Hour
	mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc

	pass := expReject(deliver(deliverMessage), true)

	n := fake.count()
	tcheck(t, deliver(strings.Replace(deliverMessage, "Subject: test", "Subject: test "+pass, 1)), "deliver with subjectpass")
	if fake.count() != n {
		t.Fatalf("rspamd consulted for message with valid subjectpass token")
	}
	ts.checkCount("Inbox", 1)

	// Once the user marks the message as not junk, the sender has reputation: a
	// reject has no hint.
	m, err := bstore.QueryDB[store.Message](ctxbg, ts.acc.DB).FilterEqual("Expunged", false).SortDesc("ID").Limit(1).Get()
	tcheck(t, err, "get delivered message")
	ts.xops.MessageFlagsAdd(ctxbg, pkglog, ts.acc, []int64{m.ID}, []string{"$NotJunk"})
	expReject(deliver(deliverMessage), false)
}

// An EHLO with an IP address literal is sent to rspamd as the address literal.
func TestRspamdHeloIPLiteral(t *testing.T) {
	resolver := dns.MockResolver{
		A:   map[string][]string{"example.org.": {"127.0.0.10"}},
		TXT: map[string][]string{"example.org.": {"v=spf1 ip4:127.0.0.10 -all"}},
		PTR: map[string][]string{"127.0.0.10": {"example.org."}},
	}
	ts := newTestServer(t, filepath.FromSlash("../testdata/smtp/mox.conf"), resolver)
	ts.tlsmode = smtpclient.TLSSkip
	defer ts.close()

	fake := &fakeRspamd{status: http.StatusOK, result: verdict("no action", false)}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: srv.URL}
	defer func() { mox.Conf.Static.Rspamd = nil }()

	ts.runRaw(func(conn net.Conn) {
		defer conn.Close()
		br := bufio.NewReader(conn)
		// response reads a, possibly multiline, response and checks its code.
		response := func(prefix string) {
			t.Helper()
			for {
				line, err := br.ReadString('\n')
				tcheck(t, err, "read")
				if !strings.HasPrefix(line, prefix) {
					t.Fatalf("got smtp response %q, expected prefix %q", line, prefix)
				}
				if len(line) < 4 || line[3] != '-' {
					return
				}
			}
		}
		write := func(s string) {
			_, err := conn.Write([]byte(s))
			tcheck(t, err, "write")
		}
		response("220")
		write("EHLO [127.0.0.10]\r\n")
		response("250")
		write("MAIL FROM:<remote@example.org>\r\n")
		response("250")
		write("RCPT TO:<mjl@mox.example>\r\n")
		response("250")
		write("DATA\r\n")
		response("354")
		write(deliverMessage + ".\r\n")
		response("250")
		write("QUIT\r\n")
	})
	fake.Lock()
	defer fake.Unlock()
	if len(fake.helos) != 1 || fake.helos[0] != "[127.0.0.10]" {
		t.Fatalf("helo sent to rspamd %q, expected [[127.0.0.10]]", fake.helos)
	}
}

// Test training rspamd through the web operations used by webmail and the webapi,
// with the user for per-user statistics from the stored Delivered-To header.
func TestRspamdTrainingWeb(t *testing.T) {
	ts := newTestServer(t, filepath.FromSlash("../testdata/smtp/mox.conf"), dns.MockResolver{})
	defer ts.close()

	type learn struct{ path, deliverTo string }
	learned := make(chan learn, 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		learned <- learn{r.URL.Path, r.Header.Get("Deliver-To")}
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: srv.URL}
	defer func() { mox.Conf.Static.Rspamd = nil }()

	dom := mox.Conf.Dynamic.Domains["mox.example"]
	dom.LocalpartCatchallSeparatorsEffective = []string{"+"}
	mox.Conf.Dynamic.Domains["mox.example"] = dom

	expect := func(l ...learn) {
		t.Helper()
		for _, exp := range l {
			select {
			case got := <-learned:
				if got != exp {
					t.Fatalf("learned %v, expected %v", got, exp)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("nothing learned, expected %v", exp)
			}
		}
		select {
		case got := <-learned:
			t.Fatalf("unexpected training %v", got)
		case <-time.After(100 * time.Millisecond):
		}
	}

	prefix := "Delivered-To: mjl+tag@mox.example\r\n"
	insert := func() int64 {
		m := store.Message{MsgPrefix: []byte(prefix), Size: int64(len(prefix) + len(deliverMessage))}
		tinsertmsg(t, ts.acc, "Inbox", &m, deliverMessage)
		return m.ID
	}
	id1, id2 := insert(), insert()
	// Imported message without Delivered-To: no user sent.
	m3 := store.Message{Size: int64(len(deliverMessage))}
	tinsertmsg(t, ts.acc, "Inbox", &m3, deliverMessage)
	expect()

	spam := learn{"/learnspam", "mjl@mox.example"}
	ham := learn{"/learnham", "mjl@mox.example"}

	// The account's own junk filter is trained as without rspamd, so muxx keeps the
	// training when rspamd is removed. rspamd follows it.
	trainedJunk := func(id int64) *bool {
		t.Helper()
		m, err := bstore.QueryDB[store.Message](ctxbg, ts.acc.DB).FilterID(id).Get()
		tcheck(t, err, "get message")
		return m.TrainedJunk
	}
	expTrained := func(id int64, junk bool) {
		t.Helper()
		if tj := trainedJunk(id); tj == nil || *tj != junk {
			t.Fatalf("message trained in junk filter %v, expected junk %v", tj, junk)
		}
	}

	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1, id2}, "Junk", 0, false)
	expect(spam, spam)
	expTrained(id1, true)
	expTrained(id2, true)
	// Without automatic junk flags, moved out of Junk keeps $Junk: still spam.
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Trash", 0, false)
	expect()
	expTrained(id1, true)

	// With automatic junk flags, a mailbox that is not junk or neutral makes it ham,
	// Trash included. A neutral mailbox untrains it in the junk filter, rspamd keeps
	// what it learned.
	acc := mox.Conf.Dynamic.Accounts[ts.acc.Name]
	acc.AutomaticJunkFlags.Enabled = true
	acc.JunkMailbox = regexp.MustCompile("^(junk|spam)")
	acc.NeutralMailbox = regexp.MustCompile("^(inbox|neutral|postmaster|dmarc|tlsrpt|rejects)")
	mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id2}, "Archive", 0, false)
	expect(ham)
	expTrained(id2, false)
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Junk", 0, false)
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Trash", 0, false)
	expect(ham)
	expTrained(id1, false)
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Inbox", 0, false)
	expect()
	if tj := trainedJunk(id1); tj != nil {
		t.Fatalf("message in neutral mailbox trained in junk filter %v", *tj)
	}
	// Taken out of Junk to a neutral mailbox: not junk, for both.
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Junk", 0, false)
	expect(spam)
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Inbox", 0, false)
	expect(ham)
	expTrained(id1, false)

	// With all three regular expressions set, a mailbox matching none keeps the flags:
	// moved from Junk to Trash, a message stays spam, in the junk filter and rspamd,
	// as documented for Rspamd.
	acc.NotJunkMailbox = regexp.MustCompile("^(archive|sent)")
	mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Junk", 0, false)
	expect(spam)
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Trash", 0, false)
	expect()
	expTrained(id1, true)
	ts.xops.MessageMove(ctxbg, pkglog, ts.acc, []int64{id1}, "Archive", 0, false)
	expect(ham)
	expTrained(id1, false)

	acc.AutomaticJunkFlags.Enabled = false
	acc.JunkMailbox, acc.NeutralMailbox, acc.NotJunkMailbox = nil, nil, nil
	mox.Conf.Dynamic.Accounts[ts.acc.Name] = acc
	ts.xops.MessageFlagsAdd(ctxbg, pkglog, ts.acc, []int64{m3.ID}, []string{"$Junk"})
	expect(learn{"/learnspam", ""})
	expTrained(m3.ID, true)
	// Adding $NotJunk with $Junk still set: neither trains. Clearing $Junk: both ham.
	ts.xops.MessageFlagsAdd(ctxbg, pkglog, ts.acc, []int64{m3.ID}, []string{"$NotJunk"})
	expect()
	if m, err := bstore.QueryDB[store.Message](ctxbg, ts.acc.DB).FilterID(m3.ID).Get(); err != nil || m.TrainedJunk != nil {
		t.Fatalf("message with $Junk and $NotJunk trained in junk filter %v, %v", m.TrainedJunk, err)
	}
	ts.xops.MessageFlagsClear(ctxbg, pkglog, ts.acc, []int64{m3.ID}, []string{"$Junk"})
	expect(learn{"/learnham", ""})
	expTrained(m3.ID, false)
	ts.xops.MessageFlagsClear(ctxbg, pkglog, ts.acc, []int64{m3.ID}, []string{"$NotJunk", "$Junk"})
	expect()
	ts.xops.MessageFlagsAdd(ctxbg, pkglog, ts.acc, []int64{m3.ID}, []string{`\Flagged`})
	expect()
}

// Symbols for the reason header: those that contributed, largest contribution
// first, at most 10.
func TestRspamdSymbols(t *testing.T) {
	if s := rspamdSymbols(nil); s != "none" {
		t.Fatalf("no symbols: %q", s)
	}
	if s := rspamdSymbols(map[string]rspamd.Symbol{"ZERO": {Name: "ZERO"}}); s != "none" {
		t.Fatalf("only zero scores: %q", s)
	}
	symbols := map[string]rspamd.Symbol{"ZERO": {Name: "ZERO"}}
	for i := 1; i <= 12; i++ {
		n := fmt.Sprintf("S%02d", i)
		score := float64(i)
		if i%2 == 0 {
			score = -score
		}
		symbols[n] = rspamd.Symbol{Name: n, Score: score}
	}
	exp := "S12(-12.00), S11(11.00), S10(-10.00), S09(9.00), S08(-8.00), S07(7.00), S06(-6.00), S05(5.00), S04(-4.00), S03(3.00) and 2 more"
	if s := rspamdSymbols(symbols); s != exp {
		t.Fatalf("got %q, expected %q", s, exp)
	}
}
