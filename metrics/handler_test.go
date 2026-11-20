package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mjl-/mox/moxvar"
)

func TestHandler(t *testing.T) {
	// Ensure at least one mox metric has a value.
	PanicInc(Ctl)

	get := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
		buf, err := io.ReadAll(rec.Result().Body)
		if err != nil {
			t.Fatalf("reading metrics: %v", err)
		}
		return string(buf)
	}

	orig := moxvar.Distribution
	defer func() { moxvar.Distribution = orig }()

	moxvar.Distribution = ""
	out := get()
	if !strings.Contains(out, "\nmox_panic_total{") || strings.Contains(out, "muxx_") {
		t.Fatalf("without distribution, expected mox_ metrics only, got:\n%s", out)
	}

	moxvar.Distribution = "muxx-202611191730"
	out = get()
	if !strings.Contains(out, "\nmuxx_panic_total{") || !strings.Contains(out, "# TYPE muxx_panic_total counter") {
		t.Fatalf("with distribution, missing muxx_panic_total, got:\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "mox_") || strings.Contains(line, " mox_") {
			t.Fatalf("with distribution, metric still named mox: %q", line)
		}
	}
	// Metrics of the Go runtime keep their names.
	if !strings.Contains(out, "\ngo_goroutines ") {
		t.Fatalf("missing go_goroutines metric")
	}
}
