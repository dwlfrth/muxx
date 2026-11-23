// Package rspamd is a client for the HTTP API of the rspamd spam filter, for
// checking incoming messages and training rspamd with messages users mark as
// (non)junk.
//
// Checks use the /checkv3 endpoint (rspamd 4.1.1 or later on the controller),
// training uses /learnspam and /learnham. See
// https://docs.rspamd.com/developers/protocol.
package rspamd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/moxvar"
)

// DefaultTimeout is used when the configuration has no timeout: twice rspamd's
// default task_timeout of 8s, after which rspamd finishes processing with what it
// has and still answers.
const DefaultTimeout = 16 * time.Second

// MinVersion is the first rspamd version whose controller serves /checkv3: the
// endpoint exists since 4.0.0, the controller routes it since 4.1.1, which
// rspamd's documentation does not mention (it lists /checkv3 for the normal
// worker and the proxy). Older controllers answer 404.
const MinVersion = "4.1.1"

// ErrTooOld is returned when rspamd does not serve /checkv3.
var ErrTooOld = errors.New("rspamd " + MinVersion + " or later is required")

// Errors for training requests rspamd declines. They are not failures.
var (
	ErrAlreadyLearned = errors.New("rspamd: message already learned")
	ErrRefused        = errors.New("rspamd: learning refused by rspamd's conditions")
)

// Action is the action rspamd recommends for a message.
type Action string

// Actions known to rspamd.
const (
	ActionNoAction       Action = "no action"
	ActionGreylist       Action = "greylist"
	ActionAddHeader      Action = "add header"
	ActionRewriteSubject Action = "rewrite subject"
	ActionSoftReject     Action = "soft reject"
	ActionReject         Action = "reject"
)

// Known returns whether the action is one of the actions known to rspamd.
func (a Action) Known() bool {
	switch a {
	case ActionNoAction, ActionGreylist, ActionAddHeader, ActionRewriteSubject, ActionSoftReject, ActionReject:
		return true
	}
	return false
}

// Symbol is a rule that matched for a message, with its contribution to the score.
type Symbol struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// Result is the verdict of rspamd for a message.
type Result struct {
	// If set, rspamd did not run all its checks: either due to its settings, with
	// Action "no action" (no verdict), or because a prefilter decided early, e.g.
	// for the GTUBE test pattern, with the verdict in Action.
	IsSkipped     bool              `json:"is_skipped"`
	Action        Action            `json:"action"`
	Score         float64           `json:"score"`
	RequiredScore float64           `json:"required_score"`
	Symbols       map[string]Symbol `json:"symbols"`
}

// Request holds the details of the SMTP transaction for a check.
type Request struct {
	IP        net.IP   // Connecting IP.
	Helo      string   // Domain or address literal from EHLO/HELO.
	Hostname  string   // Verified reverse DNS name of IP, empty if not verified.
	MailFrom  string   // SMTP MAIL FROM, empty for the null sender.
	Rcpts     []string // SMTP RCPT TO addresses.
	QueueID   string   // For correlating rspamd's logs with ours.
	DeliverTo string   // User for rspamd's per-user statistics, empty for none.
}

var (
	clientMu   sync.Mutex
	clientPool *x509.CertPool
	client     *http.Client
)

// httpClient returns the client for requests to rspamd. For https, it trusts the
// certificate authorities of mox's TLS configuration (TLS.CA), as for other HTTPS
// requests by mox, or else the system's. It keeps the default transport's proxy
// from the environment (HTTP_PROXY, HTTPS_PROXY, NO_PROXY), for setups where
// rspamd is not reached directly. It does not follow redirects: Go
// forwards custom headers such as Password to the redirect target, and for some
// redirects the message too.
func httpClient() *http.Client {
	pool := mox.Conf.Static.TLS.CertPool
	clientMu.Lock()
	defer clientMu.Unlock()
	if client == nil || pool != clientPool {
		if client != nil {
			client.CloseIdleConnections()
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{RootCAs: pool}
		client = &http.Client{
			Transport: t,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		clientPool = pool
	}
	return client
}

// Check sends msg to rspamd's /checkv3 endpoint and returns its verdict. If rspamd
// does not serve /checkv3, ErrTooOld is returned.
func Check(ctx context.Context, conf config.Rspamd, req Request, msg io.Reader) (Result, error) {
	start := time.Now()
	defer func() {
		metricCheckDuration.Observe(time.Since(start).Seconds())
	}()

	meta := map[string]any{
		"ip":   req.IP.String(),
		"from": req.MailFrom, // Empty for the null sender, which rspamd treats as "<>".
		"rcpt": req.Rcpts,
	}
	for k, v := range map[string]string{"helo": req.Helo, "hostname": req.Hostname, "queue_id": req.QueueID, "deliver_to": req.DeliverTo} {
		if v != "" {
			meta[k] = v
		}
	}
	metaBuf, err := json.Marshal(meta)
	if err != nil {
		return Result{}, fmt.Errorf("rspamd: encoding metadata: %v", err)
	}

	// Stream the message as multipart/form-data, with a "metadata" and a "message"
	// part.
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="metadata"`)
			h.Set("Content-Type", "application/json")
			w, err := mw.CreatePart(h)
			if err != nil {
				return err
			}
			if _, err := w.Write(metaBuf); err != nil {
				return err
			}
			h = textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="message"`)
			h.Set("Content-Type", "application/octet-stream") // As in rspamd's documentation.
			if w, err = mw.CreatePart(h); err != nil {
				return err
			}
			if _, err := io.Copy(w, msg); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()
	defer pr.Close() // Stops the writer if the request ends early.

	hdr := http.Header{"Content-Type": []string{mw.FormDataContentType()}}
	resp, err := do(ctx, conf, conf.Password, http.MethodPost, "/checkv3", hdr, pr)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Result{}, ErrTooOld
	} else if resp.StatusCode/100 != 2 {
		return Result{}, responseError(resp)
	}

	var r Result
	if err := decodeCheckResult(resp, &r); err != nil {
		return Result{}, err
	}
	return r, nil
}

// decodeCheckResult reads the verdict from a /checkv3 response: by default a
// multipart response with the verdict as JSON in the part named "result", and a
// part "body" only if rspamd rewrote the message, which is ignored.
func decodeCheckResult(resp *http.Response, r *Result) error {
	mt, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return fmt.Errorf("rspamd: parsing content-type of response: %v", err)
	}
	if !strings.HasPrefix(mt, "multipart/") {
		return fmt.Errorf("rspamd: unexpected content-type %q of response", mt)
	}
	mr := multipart.NewReader(resp.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return errors.New("rspamd: no result part in response")
		} else if err != nil {
			return fmt.Errorf("rspamd: reading response: %v", err)
		}
		if p.FormName() != "result" {
			continue
		}
		if err := json.NewDecoder(io.LimitReader(p, 4<<20)).Decode(r); err != nil {
			return fmt.Errorf("rspamd: parsing result: %v", err)
		}
		return nil
	}
}

// Learn trains rspamd's Bayes classifier with msg, as spam or ham. deliverTo is
// the user for per-user statistics, empty for none. ErrAlreadyLearned and
// ErrRefused are returned when rspamd declines.
func Learn(ctx context.Context, conf config.Rspamd, spam bool, deliverTo string, msg io.Reader) error {
	path := "/learnham"
	if spam {
		path = "/learnspam"
	}
	hdr := http.Header{}
	if deliverTo != "" {
		hdr.Set("Deliver-To", deliverTo)
	}
	password := conf.EnablePassword
	if password == "" {
		password = conf.Password
	}
	resp, err := do(ctx, conf, password, http.MethodPost, path, hdr, msg)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var result struct {
			Success bool `json:"success"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
			return fmt.Errorf("rspamd: parsing response: %v", err)
		} else if !result.Success {
			return errors.New("rspamd: learning not successful")
		}
		return nil
	case http.StatusNoContent:
		return ErrRefused
	case http.StatusNotFound, http.StatusAlreadyReported:
		err := responseError(resp)
		if strings.Contains(err.Error(), "already learned") {
			return ErrAlreadyLearned
		}
		return err
	}
	return responseError(resp)
}

// Stat is the part of rspamd's /stat answer shown on the admin page.
type Stat struct {
	Version string
	Bayes   []Bayes // One per binary classifier, in rspamd's order.
}

// Bayes is the training state of one binary classifier of rspamd. rspamd only
// classifies with it once both classes have MinLearns learned messages. With
// PerUser, the counts are for all users together, as rspamd reports them.
type Bayes struct {
	Name      string
	PerUser   bool
	MinLearns int64
	Spam      int64
	Ham       int64
}

// GetStat returns rspamd's version and the training state of its binary
// classifiers. In /stat, each statfile has its learned count as "revision", with
// its classifier; a statfile is spam or ham by its "class", or, without it, by its
// symbol ending in SPAM or HAM, as in rspamd's default BAYES_SPAM and BAYES_HAM.
func GetStat(ctx context.Context, conf config.Rspamd) (Stat, error) {
	resp, err := do(ctx, conf, conf.Password, http.MethodGet, "/stat", nil, nil)
	if err != nil {
		return Stat{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Stat{}, responseError(resp)
	}
	var stat struct {
		Version   string `json:"version"`
		Statfiles []struct {
			Revision   int64  `json:"revision"`
			Symbol     string `json:"symbol"`
			Class      string `json:"class"`
			Classifier struct {
				Name      string `json:"name"`
				Type      string `json:"type"`
				PerUser   bool   `json:"per_user"`
				MinLearns int64  `json:"min_learns"`
			} `json:"classifier"`
		} `json:"statfiles"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&stat); err != nil {
		return Stat{}, fmt.Errorf("rspamd: parsing stat: %v", err)
	}
	st := Stat{Version: stat.Version}
	for _, sf := range stat.Statfiles {
		cl := sf.Classifier
		if cl.Name == "" || cl.Type != "" && cl.Type != "binary" {
			continue
		}
		i := slices.IndexFunc(st.Bayes, func(b Bayes) bool { return b.Name == cl.Name })
		if i < 0 {
			st.Bayes = append(st.Bayes, Bayes{Name: cl.Name, PerUser: cl.PerUser, MinLearns: cl.MinLearns})
			i = len(st.Bayes) - 1
		}
		sym := strings.ToUpper(sf.Symbol)
		switch {
		case sf.Class == "spam" || sf.Class == "" && strings.HasSuffix(sym, "SPAM"):
			st.Bayes[i].Spam += sf.Revision
		case sf.Class == "ham" || sf.Class == "" && strings.HasSuffix(sym, "HAM"):
			st.Bayes[i].Ham += sf.Revision
		}
	}
	return st, nil
}

// TooOld returns whether version is older than MinVersion.
func TooOld(version string) bool {
	parse := func(s string) (v [3]int) {
		for i, p := range strings.SplitN(s, ".", 3) {
			fmt.Sscanf(p, "%d", &v[i])
		}
		return
	}
	v, min := parse(version), parse(MinVersion)
	for i := range v {
		if v[i] != min[i] {
			return v[i] < min[i]
		}
	}
	return false
}

// do sends a request to rspamd, with the configured timeout and password, if
// not empty. A redirect is returned as an error. The URL may appear in errors:
// the configuration check refuses URLs with credentials, a query string or a
// fragment.
func do(ctx context.Context, conf config.Rspamd, password, method, path string, hdr http.Header, body io.Reader) (*http.Response, error) {
	timeout := conf.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(conf.URL, "/")+path, body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("rspamd: new request: %v", err)
	}
	for k, l := range hdr {
		req.Header[k] = l
	}
	req.Header.Set("User-Agent", moxvar.UserAgent()+" (rspamd)")
	if password != "" {
		req.Header.Set("Password", password)
	}
	resp, err := httpClient().Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("rspamd: %w", err)
	}
	if resp.StatusCode/100 == 3 {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("rspamd: refusing redirect (http status %d) from %s", resp.StatusCode, req.URL)
	}
	resp.Body = cancelCloser{resp.Body, cancel}
	return resp, nil
}

// cancelCloser cancels the request context when the body is closed.
type cancelCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// responseError returns an error with the status and the error message from the
// response: rspamd's "error" field, or else a plain-text body of a single line of
// at most 200 characters. Other content is not used.
func responseError(resp *http.Response) error {
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(buf, &e) == nil && e.Error != "" {
		return fmt.Errorf("rspamd: http status %d: %s", resp.StatusCode, e.Error)
	}
	if s := strings.TrimSpace(string(buf)); s != "" && len(s) <= 200 && !strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("rspamd: http status %d: %s", resp.StatusCode, s)
	}
	return fmt.Errorf("rspamd: http status %d", resp.StatusCode)
}
