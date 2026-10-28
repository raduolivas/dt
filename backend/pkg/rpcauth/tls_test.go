package rpcauth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raduolivas/dt/backend/pkg/rpcauth"
)

type pki struct {
	dir string
	ca  *x509.Certificate
	key *ecdsa.PrivateKey
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	p := &pki{dir: t.TempDir(), ca: ca, key: key}
	p.write(t, "ca.pem", "CERTIFICATE", der)
	return p
}

func (p *pki) write(t *testing.T, name, typ string, der []byte) string {
	t.Helper()
	path := filepath.Join(p.dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// issue writes <name>.pem and <name>.key, valid for 127.0.0.1.
func (p *pki) issue(t *testing.T, name string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return p.write(t, name+".pem", "CERTIFICATE", der), p.write(t, name+".key", "EC PRIVATE KEY", kder)
}

func tlsServer(t *testing.T, p *pki) *httptest.Server {
	t.Helper()
	cert, key := p.issue(t, "server")
	cfg, err := rpcauth.ServerTLS(cert, key, filepath.Join(p.dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	ts.TLS = cfg
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func TestMutualTLSRejectsClientWithoutCert(t *testing.T) {
	p := newPKI(t)
	ts := tlsServer(t, p)
	pool := x509.NewCertPool()
	pool.AddCert(p.ca)
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
	resp, err := c.Get(ts.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request without a client cert succeeded")
	}
}

func TestMutualTLSAcceptsSignedClient(t *testing.T) {
	p := newPKI(t)
	ts := tlsServer(t, p)
	cert, key := p.issue(t, "client")
	cfg, err := rpcauth.ClientTLS(cert, key, filepath.Join(p.dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("client min version = %x, want TLS 1.3", cfg.MinVersion)
	}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	resp, err := c.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "ok" {
		t.Fatalf("body = %q", b)
	}
}

func TestMutualTLSRejectsClientFromOtherCA(t *testing.T) {
	ts := tlsServer(t, newPKI(t))
	other := newPKI(t)
	cert, key := other.issue(t, "client")
	cfg, err := rpcauth.ClientTLS(cert, key, filepath.Join(other.dir, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.InsecureSkipVerify = true //nolint:gosec // test: only the server's view of our cert matters
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	if resp, err := c.Get(ts.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("client signed by another CA was accepted")
	}
}

func TestTLSFromEnv(t *testing.T) {
	p := newPKI(t)
	cert, key := p.issue(t, "svc")
	ca := filepath.Join(p.dir, "ca.pem")

	set := func(c, k, a string) {
		t.Setenv("T_TLS_CERT", c)
		t.Setenv("T_TLS_KEY", k)
		t.Setenv("T_TLS_CA", a)
	}
	for name, fn := range map[string]func(string) (*tls.Config, error){
		"server": rpcauth.ServerTLSFromEnv, "client": rpcauth.ClientTLSFromEnv,
	} {
		set("", "", "")
		if cfg, err := fn("T"); cfg != nil || err != nil {
			t.Fatalf("%s: all unset = (%v, %v), want (nil, nil)", name, cfg, err)
		}
		set(cert, key, ca)
		if cfg, err := fn("T"); cfg == nil || err != nil {
			t.Fatalf("%s: all set = (%v, %v), want config", name, cfg, err)
		}
	}
}

func TestTLSFromEnvPartialIsError(t *testing.T) {
	for _, partial := range [][3]string{{"c", "", ""}, {"", "k", ""}, {"", "", "a"}, {"c", "k", ""}, {"c", "", "a"}} {
		t.Setenv("T_TLS_CERT", partial[0])
		t.Setenv("T_TLS_KEY", partial[1])
		t.Setenv("T_TLS_CA", partial[2])
		if _, err := rpcauth.ServerTLSFromEnv("T"); err == nil {
			t.Errorf("server %v: want error", partial)
		}
		if _, err := rpcauth.ClientTLSFromEnv("T"); err == nil {
			t.Errorf("client %v: want error", partial)
		}
	}
}
