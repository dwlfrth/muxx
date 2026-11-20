package mlog

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/mjl-/mox/moxvar"
)

// Log messages use the product name of a distribution, except at trace levels,
// which log protocol transcripts that must stay as sent.
func TestRenameMessages(t *testing.T) {
	SetConfig(map[string]slog.Level{"": LevelTracedata})
	defer SetConfig(map[string]slog.Level{"": LevelDebug})

	orig := moxvar.Distribution
	defer func() { moxvar.Distribution = orig }()

	capture := func(fn func(log Log)) string {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		stderr := os.Stderr
		os.Stderr = w
		fn(New("test", nil))
		os.Stderr = stderr
		w.Close()
		buf, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("reading log output: %v", err)
		}
		return string(buf)
	}
	logBoth := func(log Log) {
		log.Info("mox stopped")
		log.Debug("reading mox.conf")
		// Protocol transcripts, through Trace, which bypasses the handler's Handle, and
		// a regular log call at a trace level, which goes through Handle.
		log.Trace(LevelTrace, "S: ", []byte("220 host ESMTP mox"))
		log.Log(noctx, LevelTrace, "C: EHLO mox.example from mox")
	}

	moxvar.Distribution = ""
	out := capture(logBoth)
	if !strings.Contains(out, "mox stopped") || !strings.Contains(out, "reading mox.conf") || !strings.Contains(out, "ESMTP mox") {
		t.Fatalf("without distribution, expected unchanged messages, got:\n%s", out)
	}

	moxvar.Distribution = "muxx-202611191730"
	out = capture(logBoth)
	if !strings.Contains(out, "muxx stopped") || !strings.Contains(out, "reading muxx.conf") {
		t.Fatalf("with distribution, expected renamed messages, got:\n%s", out)
	}
	if !strings.Contains(out, "ESMTP mox") || !strings.Contains(out, "from mox") {
		t.Fatalf("with distribution, trace message changed, got:\n%s", out)
	}
}
