package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asciimoth/almagest/certcat/internal/catalog"
)

const (
	ccadbIntermediateURL = "https://ccadb.my.salesforce-sites.com/mozilla/MozillaIntermediateCertsCSVReport"
	chromiumBlocklistURL = "https://chromium.googlesource.com/chromium/src/+/main/net/data/ssl/blocklist"
)

func main() {
	outPath := flag.String("out", "data/machine.json", "output JSON path")
	skipCCADB := flag.Bool("skip-ccadb", false, "do not fetch Mozilla CCADB intermediate report")
	skipChromium := flag.Bool("skip-chromium", false, "do not fetch Chromium certificate blocklist")
	timeout := flag.Duration("http-timeout", 90*time.Second, "HTTP timeout")
	flag.Parse()

	client := &http.Client{Timeout: *timeout}
	now := time.Now().UTC().Format(time.RFC3339)

	var entries []catalog.Record
	if !*skipCCADB {
		recs, err := fetchConstrainedIntermediates(client, now)
		if err != nil {
			fatal(err)
		}
		entries = append(entries, recs...)
	}
	if !*skipChromium {
		recs, err := fetchChromiumBlocklist(client, now)
		if err != nil {
			fatal(err)
		}
		entries = append(entries, recs...)
	}

	doc := catalog.Document{
		FormatVersion: catalog.FormatVersion,
		GeneratedAt:   now,
		GeneratedBy:   "certcat-fetch",
		Entries:       entries,
	}
	if err := catalog.MachineSourcesValid(doc); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fatal(err)
	}
	if err := catalog.WriteDocument(*outPath, doc); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s with %d entries\n", *outPath, len(entries))
}

func fetchConstrainedIntermediates(client *http.Client, retrievedAt string) ([]catalog.Record, error) {
	body, err := get(client, ccadbIntermediateURL)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(bytes.NewReader(body))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read CCADB CSV: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("CCADB CSV is empty")
	}
	header := indexHeader(rows[0])
	subjectCol, ok := header["Subject"]
	if !ok {
		return nil, errors.New("CCADB CSV has no Subject column")
	}
	issuerCol, ok := header["Issuer"]
	if !ok {
		return nil, errors.New("CCADB CSV has no Issuer column")
	}
	pemCol, ok := header["PEM"]
	if !ok {
		return nil, errors.New("CCADB CSV has no PEM column")
	}

	var records []catalog.Record
	for n, row := range rows[1:] {
		if len(row) <= pemCol {
			return nil, fmt.Errorf("CCADB row %d has no PEM field", n+2)
		}
		block, _ := pem.Decode([]byte(row[pemCol]))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse CCADB row %d certificate: %w", n+2, err)
		}
		if !tlsServerCapableCA(cert) || len(cert.PermittedDNSDomains) == 0 {
			continue
		}
		certSHA256 := certSHA256(cert)
		spkiSHA256 := spkiSHA256(cert)
		subject := value(row, subjectCol)
		if subject == "" {
			subject = cert.Subject.String()
		}
		issuer := value(row, issuerCol)
		if issuer == "" {
			issuer = cert.Issuer.String()
		}
		records = append(records, catalog.Record{
			ID:                "ccadb-nameconstraints-" + strings.ToLower(certSHA256),
			Kind:              "narrow_scope_ca",
			Name:              subjectCommonName(cert),
			Subject:           subject,
			Issuer:            issuer,
			SerialHex:         strings.ToUpper(cert.SerialNumber.Text(16)),
			NotBefore:         cert.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:          cert.NotAfter.UTC().Format(time.RFC3339),
			CertificateSHA256: certSHA256,
			SPKISHA256:        spkiSHA256,
			Action:            "narrow_scope",
			Severity:          "warning",
			Recommendation:    "trust_only_for_declared_scope",
			Scope: catalog.Scope{
				Basis:                "x509_name_constraints",
				PermittedDNSSubtrees: cleanDomains(cert.PermittedDNSDomains),
				PermittedIPCIDRs:     ipNets(cert.PermittedIPRanges),
				RejectIPTargets:      len(cert.PermittedIPRanges) == 0,
			},
			Sources: []catalog.Source{
				{
					ID:          "ccadb-mozilla-intermediate-report",
					URL:         ccadbIntermediateURL,
					RetrievedAt: retrievedAt,
				},
			},
		})
	}
	return records, nil
}

func fetchChromiumBlocklist(client *http.Client, retrievedAt string) ([]catalog.Record, error) {
	body, err := get(client, chromiumBlocklistURL+"?format=JSON")
	if err != nil {
		return nil, err
	}
	body = bytes.TrimPrefix(body, []byte(")]}'\n"))
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return nil, fmt.Errorf("decode Chromium blocklist listing: %w", err)
	}
	var records []catalog.Record
	for _, entry := range listing.Entries {
		if entry.Type != "blob" {
			continue
		}
		name := entry.Name
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".pem"):
			fp := strings.TrimSuffix(lower, ".pem")
			if !isHexLen(fp, 64) {
				continue
			}
			records = append(records, chromiumRecord(
				"chromium-cert-block-"+fp,
				"certificate_sha256",
				fp,
				name,
				retrievedAt,
			))
		case strings.HasSuffix(lower, ".key"):
			fp := strings.TrimSuffix(lower, ".key")
			if !isHexLen(fp, 64) {
				continue
			}
			records = append(records, chromiumRecord(
				"chromium-spki-block-"+fp,
				"spki_sha256",
				fp,
				name,
				retrievedAt,
			))
		}
	}
	return records, nil
}

func chromiumRecord(id, matchKind, fp, filename, retrievedAt string) catalog.Record {
	rec := catalog.Record{
		ID:             id,
		Kind:           "hazard_certificate",
		Name:           "Chromium certificate blocklist entry",
		HazardClass:    "chromium_certificate_blocklist",
		Action:         "deny",
		Severity:       "high",
		Recommendation: "deny_exact_match",
		Sources: []catalog.Source{
			{
				ID:          "chromium-certificate-blocklist",
				URL:         chromiumBlocklistURL + "/README.md",
				RetrievedAt: retrievedAt,
			},
			{
				ID:          "chromium-certificate-blocklist-entry",
				URL:         chromiumBlocklistURL + "/" + filename,
				RetrievedAt: retrievedAt,
			},
		},
	}
	if matchKind == "certificate_sha256" {
		rec.CertificateSHA256 = fp
	} else {
		rec.SPKISHA256 = fp
		rec.HazardClass = "chromium_spki_blocklist"
	}
	return rec
}

func get(client *http.Client, url string) (_ []byte, err error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "almagest-certcat-fetch/1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", url, closeErr)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: HTTP %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", url, err)
	}
	return body, nil
}

func tlsServerCapableCA(cert *x509.Certificate) bool {
	if !cert.IsCA {
		return false
	}
	if len(cert.ExtKeyUsage) == 0 {
		return true
	}
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth {
			return true
		}
	}
	return false
}

func certSHA256(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func spkiSHA256(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func ipNets(in []*net.IPNet) []string {
	out := make([]string, 0, len(in))
	for _, network := range in {
		if network != nil {
			out = append(out, network.String())
		}
	}
	sort.Strings(out)
	return out
}

func cleanDomains(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		item = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(item), "."))
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func indexHeader(row []string) map[string]int {
	out := map[string]int{}
	for n, name := range row {
		out[strings.TrimSpace(name)] = n
	}
	return out
}

func value(row []string, col int) string {
	if col < 0 || col >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[col])
}

func subjectCommonName(cert *x509.Certificate) string {
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName
	}
	return cert.Subject.String()
}

func isHexLen(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
