package mqtt

import (
	"crypto/x509"
	_ "embed"
)

//go:embed certs/ca-certificates.crt
var embeddedCABundle []byte

// RootCAPool returns a certificate pool preloaded with the embedded Mozilla CA bundle.
func RootCAPool() *x509.CertPool {
	return buildRootCAs()
}

func buildRootCAs() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(embeddedCABundle)
	return pool
}
