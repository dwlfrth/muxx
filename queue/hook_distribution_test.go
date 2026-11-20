package queue

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mjl-/mox/moxvar"
)

// Webhook requests carry headers and a User-Agent with the product name of a
// distribution, and plain mox's name without.
func TestHookPostDistribution(t *testing.T) {
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
	}))
	defer srv.Close()

	orig := moxvar.Distribution
	defer func() { moxvar.Distribution = orig }()

	post := func() {
		t.Helper()
		_, _, err := HookPost(ctxbg, pkglog, 123, 2, srv.URL, "", "{}")
		tcheck(t, err, "post webhook")
	}

	moxvar.Distribution = ""
	post()
	if header.Get("X-Mox-Webhook-ID") != "123" || header.Get("X-Mox-Webhook-Attempt") != "2" || !strings.HasPrefix(header.Get("User-Agent"), "mox/") {
		t.Fatalf("without distribution, got headers %v", header)
	}

	moxvar.Distribution = "muxx-202611191730"
	post()
	if header.Get("X-Muxx-Webhook-ID") != "123" || header.Get("X-Muxx-Webhook-Attempt") != "2" || header.Get("X-Mox-Webhook-ID") != "" {
		t.Fatalf("with distribution, got headers %v", header)
	}
	if ua := header.Get("User-Agent"); !strings.HasPrefix(ua, "muxx/202611191730 (mox ") || !strings.HasSuffix(ua, ") (webhook)") {
		t.Fatalf("with distribution, got user agent %q", ua)
	}
}
