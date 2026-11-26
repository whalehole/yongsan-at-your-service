package gitlab

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
)

// Options contains explicit, per-client GitLab connection settings.
type Options struct {
	InsecureSkipVerify bool
}

func transportWithOptions(options Options) (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("the default HTTP transport cannot be cloned for GitLab TLS configuration")
	}
	transport := base.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.InsecureSkipVerify = options.InsecureSkipVerify
	return transport, nil
}

func certificateVerificationFailed(err error) bool {
	var verification *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var roots x509.SystemRootsError
	return errors.As(err, &verification) || errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname) || errors.As(err, &roots)
}

const certificateTrustGuidance = "Configure a trusted CA certificate, or explicitly set gitlab.insecure_skip_verify for a trusted private server."
