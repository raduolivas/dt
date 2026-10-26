package rpcauth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// ServerTLS returns the TLS config of an internal RPC server: TLS 1.3 only,
// the server's own certificate, and a client certificate signed by the CA in
// caFile is required and verified.
func ServerTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, pool, err := load(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

// ClientTLS returns the TLS config of an internal RPC client: TLS 1.3 only,
// the server certificate must be signed by the CA in caFile, and the client
// presents its own certificate.
func ClientTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, pool, err := load(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
	}, nil
}

// ServerTLSFromEnv is ServerTLS with the files named by <prefix>_TLS_CERT,
// <prefix>_TLS_KEY and <prefix>_TLS_CA. With all three unset it returns
// (nil, nil): the server then speaks plain HTTP (local development). Setting
// only some of them is an error.
func ServerTLSFromEnv(prefix string) (*tls.Config, error) {
	return fromEnv(prefix, ServerTLS)
}

// ClientTLSFromEnv is ClientTLS with the same environment variables as
// ServerTLSFromEnv.
func ClientTLSFromEnv(prefix string) (*tls.Config, error) {
	return fromEnv(prefix, ClientTLS)
}

func fromEnv(prefix string, build func(cert, key, ca string) (*tls.Config, error)) (*tls.Config, error) {
	names := [3]string{prefix + "_TLS_CERT", prefix + "_TLS_KEY", prefix + "_TLS_CA"}
	var vals [3]string
	set := 0
	for i, n := range names {
		if vals[i] = os.Getenv(n); vals[i] != "" {
			set++
		}
	}
	switch set {
	case 0:
		return nil, nil //nolint:nilnil // documented: no TLS configured
	case 3:
		return build(vals[0], vals[1], vals[2])
	}
	return nil, fmt.Errorf("rpcauth: %s, %s and %s must be set together", names[0], names[1], names[2])
}

func load(certFile, keyFile, caFile string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("rpcauth: load key pair: %w", err)
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // operator-supplied path
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("rpcauth: read CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return tls.Certificate{}, nil, errors.New("rpcauth: CA file holds no certificate")
	}
	return cert, pool, nil
}
