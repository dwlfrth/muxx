package webadmin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/rspamd"
)

// Test the rspamd status for the admin index page.
func TestRspamdStatus(t *testing.T) {
	mox.ConfigStaticPath = filepath.FromSlash("../testdata/webadmin/mox.conf")
	mox.ConfigDynamicPath = filepath.Join(filepath.Dir(mox.ConfigStaticPath), "domains.conf")
	mox.MustLoadConfig(true, false)
	defer func() { mox.Conf.Static.Rspamd = nil }()

	version := "4.3.0"
	statfiles := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stat" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"version":"` + version + `","statfiles":[` + statfiles + `]}`))
	}))
	defer srv.Close()

	// Not configured: nothing else is set, rspamd is not contacted.
	if s := (Admin{}).RspamdStatus(ctxbg); !reflect.DeepEqual(s, RspamdStatus{}) {
		t.Fatalf("status without rspamd configured: %#v", s)
	}

	mox.Conf.Static.Rspamd = &config.Rspamd{ControllerURL: srv.URL}
	s := Admin{}.RspamdStatus(ctxbg)
	exp := RspamdStatus{Configured: true, URL: srv.URL, Version: "4.3.0", MinVersion: rspamd.MinVersion}
	if !reflect.DeepEqual(s, exp) {
		t.Fatalf("status %#v, expected %#v", s, exp)
	}

	// Training state of rspamd's Bayes classifier, for the admin page.
	statfiles = `{"revision":12,"class":"spam","classifier":{"name":"bayes","type":"binary","per_user":true,"min_learns":200}},{"revision":548,"class":"ham","classifier":{"name":"bayes","type":"binary","per_user":true,"min_learns":200}}`
	s = Admin{}.RspamdStatus(ctxbg)
	exp.Bayes = []RspamdBayes{{Name: "bayes", PerUser: true, MinLearns: 200, Spam: 12, Ham: 548}}
	if !reflect.DeepEqual(s, exp) {
		t.Fatalf("status %#v, expected %#v", s, exp)
	}

	version = "4.1.0"
	if s := (Admin{}).RspamdStatus(ctxbg); !s.TooOld || s.Version != "4.1.0" || s.Error != "" {
		t.Fatalf("status for old rspamd: %#v", s)
	}

	// No version in the answer: unknown, not too old.
	version = ""
	if s := (Admin{}).RspamdStatus(ctxbg); s.TooOld || s.Version != "" || s.Error != "" {
		t.Fatalf("status without version: %#v", s)
	}

	srv.Close()
	if s := (Admin{}).RspamdStatus(ctxbg); s.Error == "" || s.Version != "" || !s.Configured {
		t.Fatalf("status for unreachable rspamd: %#v", s)
	}
}
