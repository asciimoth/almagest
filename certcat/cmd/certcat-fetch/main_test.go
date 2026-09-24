package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func response(status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: body, Header: make(http.Header)}
}

func TestFetchChromiumBlocklist(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	body, err := json.Marshal(map[string]any{"entries": []map[string]string{
		{"name": fingerprint + ".pem", "type": "blob"},
		{"name": strings.ToUpper(fingerprint) + ".key", "type": "blob"},
		{"name": "short.pem", "type": "blob"},
		{"name": fingerprint + ".pem", "type": "tree"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("User-Agent") != "almagest-certcat-fetch/1" {
			t.Errorf("User-Agent = %q", req.Header.Get("User-Agent"))
		}
		return response(http.StatusOK, io.NopCloser(bytes.NewReader(append([]byte(")]}'\n"), body...)))), nil
	})}

	records, err := fetchChromiumBlocklist(client, "2026-01-02T03:04:05Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].CertificateSHA256 != fingerprint || records[1].SPKISHA256 != fingerprint {
		t.Fatalf("records = %#v", records)
	}
	if records[1].HazardClass != "chromium_spki_blocklist" || len(records[0].Sources) != 2 {
		t.Fatalf("record metadata = %#v", records)
	}
}

func TestFetchConstrainedIntermediates(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "Constrained CA"},
		Issuer: pkix.Name{CommonName: "Issuer"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomainsCritical: true, PermittedDNSDomains: []string{"Example.COM", "example.com", "sub.example"},
		PermittedIPRanges: []*net.IPNet{{IP: net.ParseIP("192.0.2.0"), Mask: net.CIDRMask(24, 32)}},
		NotBefore:         time.Unix(100, 0), NotAfter: time.Unix(200, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	csv := "Subject,Issuer,PEM\n,,\"" + strings.ReplaceAll(string(pemBytes), "\n", "\n") + "\"\nignored,ignored,not-pem\n"
	client := bodyClient(csv)

	records, err := fetchConstrainedIntermediates(client, "now")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	record := records[0]
	if record.Name != "Constrained CA" || record.Subject == "" || record.Issuer == "" || record.SerialHex != "2A" {
		t.Fatalf("record identity = %#v", record)
	}
	if got := record.Scope.PermittedDNSSubtrees; len(got) != 2 || got[0] != "example.com" || got[1] != "sub.example" {
		t.Fatalf("domains = %#v", got)
	}
	if got := record.Scope.PermittedIPCIDRs; len(got) != 1 || got[0] != "192.0.2.0/24" {
		t.Fatalf("networks = %#v", got)
	}
}

func TestFetchErrors(t *testing.T) {
	tests := []struct {
		name     string
		client   *http.Client
		chromium bool
		want     string
	}{
		{"transport", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}, true, "offline"},
		{"status", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusTeapot, io.NopCloser(strings.NewReader(""))), nil
		})}, true, "HTTP I'm a teapot"},
		{"bad chromium json", bodyClient("not-json"), true, "decode Chromium"},
		{"empty csv", bodyClient(""), false, "CSV is empty"},
		{"missing subject", bodyClient("Issuer,PEM\n"), false, "no Subject"},
		{"missing issuer", bodyClient("Subject,PEM\n"), false, "no Issuer"},
		{"missing pem", bodyClient("Subject,Issuer\n"), false, "no PEM"},
		{"short row", bodyClient("Subject,Issuer,PEM\na,b\n"), false, "has no PEM field"},
		{"bad certificate", bodyClient("Subject,Issuer,PEM\na,b,\"-----BEGIN CERTIFICATE-----\nAQID\n-----END CERTIFICATE-----\"\n"), false, "parse CCADB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.chromium {
				_, err = fetchChromiumBlocklist(tt.client, "now")
			} else {
				_, err = fetchConstrainedIntermediates(tt.client, "now")
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestFetchHelpers(t *testing.T) {
	client := bodyClient("body")
	body, err := get(client, "https://example.test")
	if err != nil || string(body) != "body" {
		t.Fatalf("get() = %q, %v", body, err)
	}
	if isHexLen("xyz", 3) || !isHexLen("01aF", 4) || isHexLen("01", 4) {
		t.Fatal("isHexLen returned an incorrect result")
	}
	if got := value([]string{" a "}, 0); got != "a" || value(nil, 0) != "" || value(nil, -1) != "" {
		t.Fatal("value returned an incorrect result")
	}
	if got := indexHeader([]string{" A ", "B"}); got["A"] != 0 || got["B"] != 1 {
		t.Fatalf("indexHeader = %#v", got)
	}
	if got := cleanDomains([]string{" B. ", "a", "b", ""}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("cleanDomains = %#v", got)
	}
	if got := ipNets([]*net.IPNet{nil}); len(got) != 0 {
		t.Fatalf("ipNets = %#v", got)
	}
	if !tlsServerCapableCA(&x509.Certificate{IsCA: true}) || tlsServerCapableCA(&x509.Certificate{}) || tlsServerCapableCA(&x509.Certificate{IsCA: true, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}) {
		t.Fatal("tlsServerCapableCA returned an incorrect result")
	}
	if !tlsServerCapableCA(&x509.Certificate{IsCA: true, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}) {
		t.Fatal("ExtKeyUsageAny must be server capable")
	}
}

func bodyClient(body string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, io.NopCloser(strings.NewReader(body))), nil
	})}
}
