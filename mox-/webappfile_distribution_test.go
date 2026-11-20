package mox

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/moxvar"
)

// For builds as a distribution, served web pages have its name in their title,
// as shown before their JavaScript runs, e.g. on the login screen.
func TestWebappFileDistributionTitle(t *testing.T) {
	orig := moxvar.Distribution
	moxvar.Distribution = "muxx-202611191730"
	defer func() { moxvar.Distribution = orig }()

	a := &WebappFile{
		HTML:       []byte("<html><head><title>Mox Admin</title><style>/* css placeholder */</style></head><body><script>/* js placeholder */</script></body></html>"),
		JS:         []byte("let x = 'Mox Admin'"),
		CustomStem: "webadmin-distribution-test",
	}
	w := httptest.NewRecorder()
	a.Serve(context.Background(), mlog.New("test", nil), w, httptest.NewRequest("GET", "/admin/", nil))
	body, _ := io.ReadAll(w.Result().Body)
	page := string(body)
	if w.Code != 200 || !strings.Contains(page, "<title>Muxx Admin</title>") {
		t.Fatalf("got status %d, page %q, expected title Muxx Admin", w.Code, page)
	}
	// Only the title, not the JavaScript.
	if !strings.Contains(page, "let x = 'Mox Admin'") {
		t.Fatalf("javascript changed: %q", page)
	}
}
