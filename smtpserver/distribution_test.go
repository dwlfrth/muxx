package smtpserver

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/dns"
	"github.com/mjl-/mox/moxvar"
	"github.com/mjl-/mox/queue"
	"github.com/mjl-/mox/smtpclient"
	"github.com/mjl-/mox/store"
)

// setDistribution sets moxvar.Distribution for the duration of a test.
func setDistribution(t *testing.T, dist string) {
	t.Helper()
	orig := moxvar.Distribution
	moxvar.Distribution = dist
	t.Cleanup(func() { moxvar.Distribution = orig })
}

// The SMTP greeting and the headers mox adds to delivered messages use the product
// name of a distribution, and plain mox's name without.
func TestDistributionGreetingReason(t *testing.T) {
	// As in TestRatelimitDelivery, the message is delivered, with an X-*-Reason header.
	resolver := dns.MockResolver{
		A: map[string][]string{
			"example.org.": {"127.0.0.10"}, // For mx check.
		},
		PTR: map[string][]string{
			"127.0.0.10": {"example.org."},
		},
	}
	ts := newTestServer(t, filepath.FromSlash("../testdata/smtp/mox.conf"), resolver)
	defer ts.close()

	greeting := func() string {
		t.Helper()
		var line string
		ts.runRaw(func(conn net.Conn) {
			defer conn.Close()
			var err error
			line, err = bufio.NewReader(conn).ReadString('\n')
			tcheck(t, err, "read greeting")
		})
		return strings.TrimRight(line, "\r\n")
	}
	deliverHeader := func() string {
		t.Helper()
		ts.run(func(client *smtpclient.Client) {
			err := client.Deliver(ctxbg, "remote@example.org", "mjl@mox.example", int64(len(deliverMessage)), strings.NewReader(deliverMessage), false, false, false)
			tcheck(t, err, "deliver")
		})
		m, err := bstore.QueryDB[store.Message](ctxbg, ts.acc.DB).SortDesc("ID").Limit(1).Get()
		tcheck(t, err, "get delivered message")
		buf, err := io.ReadAll(ts.acc.MessageReader(m))
		tcheck(t, err, "read message")
		header, _, _ := strings.Cut(string(buf), "\r\n\r\n")
		// Each header line starts after a line break, also the first.
		return "\r\n" + header
	}

	setDistribution(t, "")
	if g := greeting(); !strings.HasSuffix(g, " ESMTP mox") {
		t.Fatalf("got greeting %q, expected ESMTP mox", g)
	}
	if h := deliverHeader(); !strings.Contains(h, "\r\nX-Mox-Reason: ") {
		t.Fatalf("missing X-Mox-Reason header in:\n%s", h)
	}

	setDistribution(t, "muxx-202611191730")
	if g := greeting(); !strings.HasSuffix(g, " ESMTP muxx") {
		t.Fatalf("got greeting %q, expected ESMTP muxx", g)
	}
	if h := deliverHeader(); !strings.Contains(h, "\r\nX-Muxx-Reason: ") || strings.Contains(h, "X-Mox-Reason") {
		t.Fatalf("expected X-Muxx-Reason header and no X-Mox-Reason in:\n%s", h)
	}
}

// Metadata headers of submitted messages are recognized with the product name of a
// distribution.
func TestDistributionExtra(t *testing.T) {
	ts := newTestServer(t, filepath.FromSlash("../testdata/smtp/mox.conf"), dns.MockResolver{})
	defer ts.close()
	ts.user = "mjl@mox.example"
	ts.pass = password0
	ts.submission = true

	submit := func(header string) map[string]string {
		t.Helper()
		msg := strings.ReplaceAll("From: <mjl@mox.example>\nTo: <remote@example.org>\nSubject: test\n"+header+"\nMessage-Id: <test@mox.example>\n\ntest email\n", "\n", "\r\n")
		ts.run(func(client *smtpclient.Client) {
			err := client.Deliver(ctxbg, "mjl@mox.example", "mjl@mox.example", int64(len(msg)), strings.NewReader(msg), true, true, false)
			tcheck(t, err, "deliver")
		})
		msgs, err := queue.List(ctxbg, queue.Filter{}, queue.Sort{Field: "Queued", Asc: false})
		tcheck(t, err, "queue list")
		return msgs[0].Extra
	}

	setDistribution(t, "muxx-202611191730")
	tcompare(t, submit("X-Muxx-Extra-Order: 1"), map[string]string{"Order": "1"})
	// The plain mox header is not metadata in a distribution.
	if extra := submit("X-Mox-Extra-Order: 2"); len(extra) != 0 {
		t.Fatalf("got extra %v for X-Mox-Extra header in distribution, expected none", extra)
	}
}
