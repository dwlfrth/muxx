package mox

import (
	"crypto"
	"fmt"
	"testing"

	"github.com/mjl-/autocert"

	"github.com/mjl-/mox/autotls"
	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/dns"
	"github.com/mjl-/mox/mlog"
)

// Send-only domains have no autoconfig DNS records, so no TLS certificate is
// requested for their autoconfig host name.
func TestAllowACMEHostsSendOnly(t *testing.T) {
	log := mlog.New("mox", nil)
	shutdown := make(chan struct{})
	defer close(shutdown)
	getPrivateKey := func(host string, keyType autocert.KeyType) (crypto.Signer, error) {
		return nil, fmt.Errorf("not used")
	}
	m, err := autotls.Load(log, "test", t.TempDir(), "mox@localhost", "https://localhost/", "", nil, getPrivateKey, shutdown)
	if err != nil {
		t.Fatalf("load acme manager: %v", err)
	}

	var l config.Listener
	l.TLS = &config.TLS{ACME: "test"}
	l.AutoconfigHTTPS.Enabled = true
	c := &Config{
		Static: config.Static{
			HostnameDomain: dns.Domain{ASCII: "mail.mox.example"},
			Listeners:      map[string]config.Listener{"public": l},
			ACME:           map[string]config.ACME{"test": {Manager: m}},
		},
		Dynamic: config.Dynamic{
			Domains: map[string]config.Domain{
				"receive.example":  {Domain: dns.Domain{ASCII: "receive.example"}},
				"sendonly.example": {Domain: dns.Domain{ASCII: "sendonly.example"}, SendOnly: true},
			},
		},
	}
	c.allowACMEHosts(log, false)

	hosts := map[string]bool{}
	for _, d := range m.Hostnames() {
		hosts[d.ASCII] = true
	}
	if !hosts["autoconfig.receive.example"] {
		t.Fatalf("missing autoconfig host for regular domain, got %v", hosts)
	}
	if hosts["autoconfig.sendonly.example"] {
		t.Fatalf("got autoconfig host for send-only domain: %v", hosts)
	}
}
