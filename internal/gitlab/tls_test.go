package gitlab

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitLabTLSDefaultRejectsSelfSignedAndExplicitBypassIsIsolated(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("bypassed TLS request lost its configured token")
		}
		writeReference(w, commitA)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	defaultTransport := http.DefaultTransport.(*http.Transport)
	secure, err := New(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer secure.HTTPClient.CloseIdleConnections()
	// Transport.Clone initializes the standard library's HTTP/2 defaults.
	// Capture the baseline after that lazy initialization, before any bypass.
	defaultConfig := defaultTransport.TLSClientConfig
	_, err = secure.ResolveBranch(context.Background(), "group/service", "main")
	var verification *tls.CertificateVerificationError
	if err == nil || !errors.As(err, &verification) || !strings.Contains(err.Error(), "TLS certificate could not be verified") || !strings.Contains(err.Error(), "gitlab.insecure_skip_verify") {
		t.Fatalf("secure default did not explain untrusted certificate: %v", err)
	}
	insecure, err := NewWithOptions(server.URL, "test-token", Options{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer insecure.HTTPClient.CloseIdleConnections()
	sha, err := insecure.ResolveBranch(context.Background(), "group/service", "main")
	if err != nil || sha != commitA || requests.Load() != 1 {
		t.Fatalf("explicit TLS bypass did not work: SHA=%q error=%v requests=%d", sha, err, requests.Load())
	}
	secureAgain, err := NewWithOptions(server.URL, "test-token", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer secureAgain.HTTPClient.CloseIdleConnections()
	for name, client := range map[string]*http.Client{"new default client": secureAgain.HTTPClient, "global default client": http.DefaultClient} {
		resp, err := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
		var verification *tls.CertificateVerificationError
		if err == nil || !errors.As(err, &verification) {
			t.Fatalf("%s became insecure after another client's bypass: %v", name, err)
		}
	}
	if http.DefaultTransport != defaultTransport || defaultTransport.TLSClientConfig != defaultConfig || defaultConfig != nil && defaultConfig.InsecureSkipVerify {
		t.Fatal("per-client TLS bypass mutated the global default transport")
	}
	if requests.Load() != 1 {
		t.Fatalf("secure clients sent HTTP requests over an untrusted connection: %d", requests.Load())
	}
}

func TestGitLabTLSOptionsCloneExistingTLSConfiguration(t *testing.T) {
	original := http.DefaultTransport
	base := original.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	http.DefaultTransport = base
	t.Cleanup(func() { http.DefaultTransport = original; base.CloseIdleConnections() })
	insecure, err := NewWithOptions("https://gitlab.example.com", "test-token", Options{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	secure, err := New("https://gitlab.example.com", "test-token")
	if err != nil {
		t.Fatal(err)
	}
	defer insecure.HTTPClient.CloseIdleConnections()
	defer secure.HTTPClient.CloseIdleConnections()
	insecureTransport := insecure.HTTPClient.Transport.(*http.Transport)
	secureTransport := secure.HTTPClient.Transport.(*http.Transport)
	if insecureTransport == base || secureTransport == base || insecureTransport == secureTransport || insecureTransport.TLSClientConfig == base.TLSClientConfig || secureTransport.TLSClientConfig == base.TLSClientConfig || insecureTransport.TLSClientConfig == secureTransport.TLSClientConfig {
		t.Fatal("GitLab clients share mutable transport or TLS configuration")
	}
	if !insecureTransport.TLSClientConfig.InsecureSkipVerify || secureTransport.TLSClientConfig.InsecureSkipVerify || base.TLSClientConfig.InsecureSkipVerify || insecureTransport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS option changed the default policy or discarded existing transport settings")
	}
}

func TestGitLabTLSBypassStillRejectsRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		writeReference(w, commitA)
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client, err := NewWithOptions(source.URL, "test-token", Options{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.HTTPClient.CloseIdleConnections()
	client.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	_, err = client.ResolveBranch(context.Background(), "group/service", "main")
	var response *HTTPError
	if !errors.As(err, &response) || response.StatusCode != http.StatusFound || redirected.Load() != 0 {
		t.Fatalf("TLS bypass weakened redirect protection: %v; target requests=%d", err, redirected.Load())
	}
}

func TestGitLabTLSCertificateErrorPreservesCauseWithoutAmbiguousWrite(t *testing.T) {
	verification := &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{Cert: &x509.Certificate{DNSNames: []string{"private-host"}}}}
	cause := fmt.Errorf("private diagnostics test-token: %w", verification)
	for _, write := range []bool{false, true} {
		err := actionError(`contact repository "group/service"`, cause, write)
		var found *tls.CertificateVerificationError
		var authority x509.UnknownAuthorityError
		if !errors.As(err, &found) || found != verification || !errors.As(err, &authority) {
			t.Fatalf("TLS verification cause lost: %v", err)
		}
		for _, want := range []string{"TLS certificate could not be verified", "trusted CA certificate", "gitlab.insecure_skip_verify"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("message %q lacks %q", err, want)
			}
		}
		for _, unwanted := range []string{"private diagnostics", "test-token", "private-host", "may have reached GitLab", "result could not be confirmed"} {
			if strings.Contains(err.Error(), unwanted) {
				t.Errorf("unsafe TLS message: %v", err)
			}
		}
	}
}
