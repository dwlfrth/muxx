package rspamd

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/moxvar"
)

const testMsg = "From: <remote@example.org>\r\nSubject: test\r\n\r\nbody\r\n"

// writeV3Result writes a /checkv3 response like rspamd's default: multipart with
// a "result" part, and optionally a "body" part.
func writeV3Result(w http.ResponseWriter, result string, withBody bool) {
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	if withBody {
		pw, _ := mw.CreateFormFile("body", "body")
		pw.Write([]byte("rewritten"))
	}
	pw, _ := mw.CreateFormField("result")
	pw.Write([]byte(result))
	mw.Close()
	w.Header().Set("Content-Type", mw.FormDataContentType())
	w.Write([]byte(buf.String()))
}

func TestCheck(t *testing.T) {
	var gotMeta map[string]any
	var gotMsg, gotPassword, gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/checkv3" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		gotPassword = r.Header.Get("Password")
		gotUserAgent = r.Header.Get("User-Agent")
		gotMeta = nil
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "multipart/form-data" {
			t.Errorf("content-type %q", r.Header.Get("Content-Type"))
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("next part: %v", err)
			}
			buf, _ := io.ReadAll(p)
			switch p.FormName() {
			case "metadata":
				if err := json.Unmarshal(buf, &gotMeta); err != nil {
					t.Fatalf("parsing metadata: %v", err)
				}
			case "message":
				gotMsg = string(buf)
			default:
				t.Errorf("unexpected part %q", p.FormName())
			}
		}
		writeV3Result(w, `{"is_skipped":false,"action":"add header","score":7.5,"required_score":15,"symbols":{"BAYES_SPAM":{"name":"BAYES_SPAM","score":5.1}}}`, true)
	}))
	defer srv.Close()

	conf := config.Rspamd{ControllerURL: srv.URL + "/", Password: "secret"}
	req := Request{
		IP:        net.ParseIP("192.0.2.1"),
		Helo:      "mail.example.org",
		MailFrom:  "", // Null sender.
		Rcpts:     []string{"mjl@mox.example"},
		QueueID:   "abc",
		DeliverTo: "mjl@mox.example",
	}
	r, err := Check(context.Background(), conf, req, strings.NewReader(testMsg))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if r.Action != ActionAddHeader || r.Score != 7.5 || r.RequiredScore != 15 || r.Symbols["BAYES_SPAM"].Score != 5.1 {
		t.Fatalf("unexpected result %#v", r)
	}
	if gotMsg != testMsg {
		t.Fatalf("message sent %q, expected %q", gotMsg, testMsg)
	}
	if gotPassword != "secret" {
		t.Fatalf("password %q", gotPassword)
	}
	if exp := "mox/" + moxvar.Version + " (rspamd)"; gotUserAgent != exp {
		t.Fatalf("user-agent %q, expected %q", gotUserAgent, exp)
	}
	// A muxx build names muxx and its release, as mox's other HTTP clients do.
	func() {
		orig := moxvar.Distribution
		defer func() { moxvar.Distribution = orig }()
		moxvar.Distribution = "muxx-202611191730"
		if _, err := Check(context.Background(), conf, req, strings.NewReader(testMsg)); err != nil {
			t.Fatalf("check: %v", err)
		}
		if exp := moxvar.UserAgent() + " (rspamd)"; gotUserAgent != exp || !strings.HasPrefix(gotUserAgent, "muxx/") {
			t.Fatalf("user-agent %q, expected %q", gotUserAgent, exp)
		}
	}()
	exp := map[string]any{"ip": "192.0.2.1", "helo": "mail.example.org", "from": "", "rcpt": []any{"mjl@mox.example"}, "queue_id": "abc", "deliver_to": "mjl@mox.example"}
	if fmt.Sprint(gotMeta) != fmt.Sprint(exp) {
		t.Fatalf("metadata %v, expected %v", gotMeta, exp)
	}

	// Hostname, when known, is sent; empty values are left out.
	req.Hostname = "mail.example.org"
	req.DeliverTo = ""
	if _, err := Check(context.Background(), conf, req, strings.NewReader(testMsg)); err != nil {
		t.Fatalf("check: %v", err)
	}
	if gotMeta["hostname"] != "mail.example.org" {
		t.Fatalf("hostname not sent: %v", gotMeta)
	}
	if _, ok := gotMeta["deliver_to"]; ok {
		t.Fatalf("empty deliver_to sent: %v", gotMeta)
	}
}

func TestCheckErrors(t *testing.T) {
	var status int
	var contentType, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	conf := config.Rspamd{ControllerURL: srv.URL}
	req := Request{IP: net.ParseIP("192.0.2.1")}

	check := func(s int, ct, b string, expErr error, expText string) {
		t.Helper()
		status, contentType, body = s, ct, b
		_, err := Check(context.Background(), conf, req, strings.NewReader(testMsg))
		if err == nil || expErr != nil && !errors.Is(err, expErr) || expText != "" && !strings.Contains(err.Error(), expText) {
			t.Fatalf("got err %v, expected %v %q", err, expErr, expText)
		}
	}

	// Controllers before rspamd 4.1.1 don't route /checkv3.
	check(http.StatusNotFound, "text/plain", "Not found", ErrTooOld, "")
	check(http.StatusInternalServerError, "application/json", `{"error":"cannot parse multipart/form-data body","error_domain":"protocol"}`, nil, "cannot parse")
	// Only rspamd's error field is quoted, not other content.
	check(http.StatusUnauthorized, "application/json", `{"error":"Unauthorized","other":"x-secret-x"}`, nil, "Unauthorized")
	// A success response without a result part.
	check(http.StatusOK, "multipart/form-data; boundary=x", "--x--\r\n", nil, "no result part")
	check(http.StatusOK, "application/json", `{}`, nil, "unexpected content-type")
}

func TestRedirectRefused(t *testing.T) {
	var targetHit atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Add(1)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	conf := config.Rspamd{ControllerURL: srv.URL, Password: "secret"}
	_, err := Check(context.Background(), conf, Request{IP: net.ParseIP("192.0.2.1")}, strings.NewReader(testMsg))
	if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("got %v, expected redirect refused", err)
	}
	err = Learn(context.Background(), conf, true, "", strings.NewReader(testMsg))
	if err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Fatalf("got %v, expected redirect refused", err)
	}
	if n := targetHit.Load(); n != 0 {
		t.Fatalf("redirect target received %d requests, with the password", n)
	}
}

// For https, the certificate authorities of mox's TLS configuration are trusted.
func TestTLSCertPool(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"4.3.0"}`))
	}))
	defer srv.Close()
	conf := config.Rspamd{ControllerURL: srv.URL}
	defer func() { mox.Conf.Static.TLS.CertPool = nil }()

	// Not trusted by the system's certificate authorities.
	mox.Conf.Static.TLS.CertPool = nil
	if _, err := GetStat(context.Background(), conf); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("got %v, expected certificate error", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	mox.Conf.Static.TLS.CertPool = pool
	if st, err := GetStat(context.Background(), conf); err != nil || st.Version != "4.3.0" {
		t.Fatalf("got %q, %v, expected version with configured certificate authority", st.Version, err)
	}
}

// Training sends EnablePassword if set, checks and status requests send Password.
func TestPasswords(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		got[r.URL.Path] = r.Header.Get("Password")
		mu.Unlock()
		http.Error(w, "test", http.StatusInternalServerError)
	}))
	defer srv.Close()

	check := func(conf config.Rspamd, expCheck, expLearn string) {
		t.Helper()
		clear(got)
		Check(context.Background(), conf, Request{IP: net.ParseIP("192.0.2.1")}, strings.NewReader(testMsg))
		GetStat(context.Background(), conf)
		Learn(context.Background(), conf, true, "", strings.NewReader(testMsg))
		Learn(context.Background(), conf, false, "", strings.NewReader(testMsg))
		exp := map[string]string{"/checkv3": expCheck, "/stat": expCheck, "/learnspam": expLearn, "/learnham": expLearn}
		if !maps.Equal(got, exp) {
			t.Fatalf("passwords sent %v, expected %v", got, exp)
		}
	}
	check(config.Rspamd{ControllerURL: srv.URL, Password: "readonly", EnablePassword: "enable"}, "readonly", "enable")
	check(config.Rspamd{ControllerURL: srv.URL, Password: "both"}, "both", "both")
	check(config.Rspamd{ControllerURL: srv.URL}, "", "")
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	conf := config.Rspamd{ControllerURL: srv.URL, Timeout: 50 * time.Millisecond}
	t0 := time.Now()
	_, err := Check(context.Background(), conf, Request{IP: net.ParseIP("192.0.2.1")}, strings.NewReader(testMsg))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, expected deadline exceeded", err)
	}
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Fatalf("timeout not applied, took %v", d)
	}
}

func TestLearn(t *testing.T) {
	var status int
	var body, gotPath, gotDeliverTo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotDeliverTo = r.Header.Get("Deliver-To")
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	conf := config.Rspamd{ControllerURL: srv.URL}

	learn := func(spam bool, s int, b string, expErr error, expPath string) {
		t.Helper()
		status, body = s, b
		err := Learn(context.Background(), conf, spam, "mjl@mox.example", strings.NewReader(testMsg))
		if expErr == nil && err != nil || expErr != nil && !errors.Is(err, expErr) {
			t.Fatalf("got %v, expected %v", err, expErr)
		}
		if gotPath != expPath || gotDeliverTo != "mjl@mox.example" {
			t.Fatalf("path %q deliver-to %q", gotPath, gotDeliverTo)
		}
	}
	learn(true, http.StatusOK, `{"success":true}`, nil, "/learnspam")
	learn(false, http.StatusOK, `{"success":true}`, nil, "/learnham")
	learn(true, http.StatusNotFound, `{"error":"<abc> has been already learned as spam, ignore it"}`, ErrAlreadyLearned, "/learnspam")
	learn(true, http.StatusAlreadyReported, `{"error":"<abc> has been already learned as spam, ignore it"}`, ErrAlreadyLearned, "/learnspam")
	learn(true, http.StatusNoContent, "", ErrRefused, "/learnspam")

	status, body = http.StatusUnauthorized, `{"error":"Unauthorized"}`
	if err := Learn(context.Background(), conf, true, "", strings.NewReader(testMsg)); err == nil || errors.Is(err, ErrAlreadyLearned) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v, expected 401 failure", err)
	}
}

func TestStat(t *testing.T) {
	// Shape of rspamd's /stat: statfiles with their learned count as revision and
	// their classifier (src/libstat/stat_process.c, rspamd_stat_statistics). The
	// "bayes" statfiles have a class, as in rspamd 4; "old" has only symbols; "multi"
	// is not binary and is left out.
	stat := `{"version":"4.3.0","scanned":10,"statfiles":[
		{"revision":12,"symbol":"BAYES_SPAM","class":"spam","classifier":{"name":"bayes","type":"binary","per_user":false,"min_learns":200}},
		{"revision":548,"symbol":"BAYES_HAM","class":"ham","classifier":{"name":"bayes","type":"binary","per_user":false,"min_learns":200}},
		{"revision":3,"symbol":"OLD_SPAM","classifier":{"name":"old","per_user":true,"min_learns":10}},
		{"revision":4,"symbol":"OLD_HAM","classifier":{"name":"old","per_user":true,"min_learns":10}},
		{"revision":9,"symbol":"M_NEWS","class":"news","classifier":{"name":"multi","type":"multi-class","min_learns":5}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stat" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(stat))
	}))
	defer srv.Close()
	st, err := GetStat(context.Background(), config.Rspamd{ControllerURL: srv.URL})
	exp := Stat{Version: "4.3.0", Bayes: []Bayes{
		{Name: "bayes", MinLearns: 200, Spam: 12, Ham: 548},
		{Name: "old", PerUser: true, MinLearns: 10, Spam: 3, Ham: 4},
	}}
	if err != nil || !reflect.DeepEqual(st, exp) {
		t.Fatalf("got %#v %v, expected %#v", st, err, exp)
	}

	// Without statistics, as from rspamd without a classifier: only the version.
	stat = `{"version":"4.3.0"}`
	if st, err := GetStat(context.Background(), config.Rspamd{ControllerURL: srv.URL}); err != nil || st.Version != "4.3.0" || st.Bayes != nil {
		t.Fatalf("got %#v %v", st, err)
	}

	for v, exp := range map[string]bool{"4.3.0": false, "4.1.1": false, "4.1.0": true, "3.14.3": true, "10.0": false, "4.2": false, "": true} {
		if TooOld(v) != exp {
			t.Errorf("TooOld(%q) = %v, expected %v", v, !exp, exp)
		}
	}
}

// closeCounter fails on a second close, like mox's message reader, which logged
// "msg is closed" when the HTTP client also closed the request body.
type closeCounter struct {
	io.Reader
	closes atomic.Int32
}

func (c *closeCounter) Close() error {
	if c.closes.Add(1) > 1 {
		return errors.New("msg is closed")
	}
	return nil
}

func TestLearnQueue(t *testing.T) {
	learned := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		w.Write([]byte(`{"success":true}`))
		learned <- r.URL.Path + " " + string(buf)
	}))
	defer srv.Close()
	conf := config.Rspamd{ControllerURL: srv.URL}
	log := mlog.New("rspamd", nil)

	cc := &closeCounter{Reader: strings.NewReader(testMsg)}
	opened := make(chan struct{}, 1)
	QueueLearn(LearnJob{Log: log, Conf: conf, Spam: true, Open: func() (io.ReadCloser, error) {
		opened <- struct{}{}
		return cc, nil
	}})
	select {
	case s := <-learned:
		if s != "/learnspam "+testMsg {
			t.Fatalf("learned %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("message not learned")
	}
	<-opened
	// Wait for the worker to finish with the job.
	for i := 0; i < 100 && cc.closes.Load() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if n := cc.closes.Load(); n != 1 {
		t.Fatalf("message closed %d times, expected once", n)
	}

	// A message removed before sending is dropped, not sent.
	done := make(chan struct{})
	QueueLearn(LearnJob{Log: log, Conf: conf, Spam: false, Open: func() (io.ReadCloser, error) {
		defer close(done)
		return nil, fmt.Errorf("open: %w", fs.ErrNotExist)
	}})
	<-done
	select {
	case s := <-learned:
		t.Fatalf("removed message was sent: %q", s)
	case <-time.After(100 * time.Millisecond):
	}
}

// learnCount returns the value of the training counter for result.
func learnCount(t *testing.T, result string) float64 {
	t.Helper()
	var m dto.Metric
	if err := metricLearn.WithLabelValues(result).Write(&m); err != nil {
		t.Fatalf("reading metric: %v", err)
	}
	return m.GetCounter().GetValue()
}

// Each outcome of a training job is counted under its own result.
func TestLearnResults(t *testing.T) {
	var status int
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	log := mlog.New("rspamd", nil)
	conf := config.Rspamd{ControllerURL: srv.URL}
	openMsg := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(testMsg)), nil }

	check := func(open func() (io.ReadCloser, error), s int, b, expResult string) {
		t.Helper()
		status, body = s, b
		before := map[string]float64{}
		for _, r := range []string{learnOK, learnAlready, learnRefused, learnFailed, learnDropped} {
			before[r] = learnCount(t, r)
		}
		learnOne(LearnJob{Log: log, Conf: conf, Spam: true, Open: open})
		for r, n := range before {
			exp := n
			if r == expResult {
				exp++
			}
			if got := learnCount(t, r); got != exp {
				t.Fatalf("result %q counted %v, expected %v (expected result %q)", r, got, exp, expResult)
			}
		}
	}
	check(openMsg, http.StatusOK, `{"success":true}`, learnOK)
	check(openMsg, http.StatusAlreadyReported, `{"error":"<abc> has been already learned as spam, ignore it"}`, learnAlready)
	check(openMsg, http.StatusNoContent, "", learnRefused)
	check(openMsg, http.StatusInternalServerError, `{"error":"internal"}`, learnFailed)
	check(openMsg, http.StatusOK, `{"success":false}`, learnFailed)
	check(func() (io.ReadCloser, error) { return nil, fmt.Errorf("open: %w", fs.ErrNotExist) }, http.StatusOK, "", learnDropped)
	check(func() (io.ReadCloser, error) { return nil, errors.New("disk error") }, http.StatusOK, "", learnFailed)
	check(func() (io.ReadCloser, error) { panic("boom") }, http.StatusOK, "", learnFailed)
}

// With the queue full, a message is dropped and counted.
func TestLearnQueueFull(t *testing.T) {
	log := mlog.New("rspamd", nil)
	conf := config.Rspamd{ControllerURL: "http://localhost:1"} // Never contacted.
	removed := func() (io.ReadCloser, error) { return nil, fs.ErrNotExist }
	base := learnCount(t, learnDropped)

	// Keep the worker busy with one job, so the queue only fills.
	busy := make(chan struct{})
	release := make(chan struct{})
	QueueLearn(LearnJob{Log: log, Conf: conf, Open: func() (io.ReadCloser, error) {
		close(busy)
		<-release
		return nil, fs.ErrNotExist
	}})
	<-busy
	for range LearnQueueSize {
		QueueLearn(LearnJob{Log: log, Conf: conf, Open: removed})
	}
	if n := LearnQueueLength(); n != LearnQueueSize {
		close(release)
		t.Fatalf("queue length %d, expected %d", n, LearnQueueSize)
	}
	dropped := learnCount(t, learnDropped)
	QueueLearn(LearnJob{Log: log, Conf: conf, Open: removed})
	got := learnCount(t, learnDropped)
	close(release)
	if got != dropped+1 {
		t.Fatalf("dropped counted %v, expected %v", got, dropped+1)
	}
	// Wait until the worker has finished every job, so none is counted during other
	// tests: the busy one, the full queue, all removed (dropped), plus the one dropped
	// for the full queue.
	exp := base + 1 + LearnQueueSize + 1
	for i := 0; i < 1000 && learnCount(t, learnDropped) < exp; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if got := learnCount(t, learnDropped); got != exp {
		t.Fatalf("dropped counted %v after the queue emptied, expected %v", got, exp)
	}
}
