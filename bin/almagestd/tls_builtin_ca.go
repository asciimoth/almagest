package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/kataras/golog"
)

const (
	tlsBuiltinCACertName   = "builtin-ca:almagest"
	tlsBuiltinCACertPrefix = "builtin-ca:"

	tlsBuiltinCADirName   = "tls-builtin-ca"
	tlsBuiltinCACertFile  = "ca.pem"
	tlsBuiltinCAKeyFile   = "ca-key.pem"
	tlsBuiltinCAStateFile = "state.json"

	tlsBuiltinCADefaultIssuer       = "Almagest"
	tlsBuiltinCADefaultKeyAlgorithm = "ecdsa"
	tlsBuiltinCADefaultCurve        = "P256"
	tlsBuiltinCADefaultRSAKeyBits   = 3072
	tlsBuiltinCAMinRSAKeyBits       = 2048
	tlsBuiltinCAMaxRSAKeyBits       = 8192
	tlsBuiltinCAMaxIssuerRunes      = 256
)

var (
	errTLSBuiltinCANotFound = errors.New("builtin TLS CA was not found")
	errTLSBuiltinCAInvalid  = errors.New("builtin TLS CA is invalid")

	tlsBuiltinCANoExpirationNotAfter = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
)

type tlsBuiltinCA struct {
	certPEM       string
	privateKeyPEM string
	signer        crypto.Signer
	certificate   *x509.Certificate
	options       api.TLSBuiltinCAOptions
	immutable     bool
}

type tlsBuiltinCAState struct {
	Version   int                     `json:"version"`
	Options   api.TLSBuiltinCAOptions `json:"options"`
	CreatedAt string                  `json:"created_at,omitempty"`
}

func cloneTLSBuiltinCA(in *tlsBuiltinCA) *tlsBuiltinCA {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func withTLSBuiltinCA(data immutableTLSData, ca *tlsBuiltinCA) immutableTLSData {
	if ca == nil {
		return data
	}
	if data.Certs == nil {
		data.Certs = map[string]immutableTLSCert{}
	}
	data.Certs[tlsBuiltinCACertName] = immutableTLSCert{
		tlsCertificateRecord: tlsCertificateRecord{PEM: ca.certPEM},
		source:               api.TLSSourceBuiltin,
	}
	return data
}

func defaultTLSBuiltinCAOptions() api.TLSBuiltinCAOptions {
	return api.TLSBuiltinCAOptions{
		Issuer:       tlsBuiltinCADefaultIssuer,
		KeyAlgorithm: tlsBuiltinCADefaultKeyAlgorithm,
		Curve:        tlsBuiltinCADefaultCurve,
	}
}

func normalizeTLSBuiltinCAOptions(in api.TLSBuiltinCAOptions) (api.TLSBuiltinCAOptions, error) {
	out := in
	out.Issuer = strings.TrimSpace(out.Issuer)
	if out.Issuer == "" {
		out.Issuer = tlsBuiltinCADefaultIssuer
	}
	if !utf8.ValidString(out.Issuer) {
		return api.TLSBuiltinCAOptions{}, fmt.Errorf("%w: builtin TLS CA issuer is not valid UTF-8", almagest.ErrTlsConfigInvalidValue)
	}
	if len([]rune(out.Issuer)) > tlsBuiltinCAMaxIssuerRunes {
		return api.TLSBuiltinCAOptions{}, fmt.Errorf("%w: builtin TLS CA issuer is longer than %d characters", almagest.ErrTlsConfigInvalidValue, tlsBuiltinCAMaxIssuerRunes)
	}
	if out.ValidityDays < 0 {
		return api.TLSBuiltinCAOptions{}, fmt.Errorf("%w: builtin TLS CA validity days cannot be negative", almagest.ErrTlsConfigInvalidValue)
	}

	out.KeyAlgorithm = strings.ToLower(strings.TrimSpace(out.KeyAlgorithm))
	if out.KeyAlgorithm == "" {
		out.KeyAlgorithm = tlsBuiltinCADefaultKeyAlgorithm
	}
	switch out.KeyAlgorithm {
	case "ecdsa":
		curve, err := normalizeTLSBuiltinCACurve(out.Curve)
		if err != nil {
			return api.TLSBuiltinCAOptions{}, err
		}
		out.Curve = curve
		out.KeyBits = 0
	case "ed25519":
		out.Curve = ""
		out.KeyBits = 0
	case "rsa":
		out.Curve = ""
		if out.KeyBits == 0 {
			out.KeyBits = tlsBuiltinCADefaultRSAKeyBits
		}
		if out.KeyBits < tlsBuiltinCAMinRSAKeyBits || out.KeyBits > tlsBuiltinCAMaxRSAKeyBits {
			return api.TLSBuiltinCAOptions{}, fmt.Errorf(
				"%w: builtin TLS CA RSA key bits must be between %d and %d",
				almagest.ErrTlsConfigInvalidValue,
				tlsBuiltinCAMinRSAKeyBits,
				tlsBuiltinCAMaxRSAKeyBits,
			)
		}
	default:
		return api.TLSBuiltinCAOptions{}, fmt.Errorf("%w: unsupported builtin TLS CA key algorithm %q", almagest.ErrTlsConfigInvalidValue, in.KeyAlgorithm)
	}
	return out, nil
}

func normalizeTLSBuiltinCACurve(value string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "")) {
	case "", "p256", "prime256v1", "secp256r1":
		return "P256", nil
	case "p384", "secp384r1":
		return "P384", nil
	case "p521", "secp521r1":
		return "P521", nil
	default:
		return "", fmt.Errorf("%w: unsupported builtin TLS CA ECDSA curve %q", almagest.ErrTlsConfigInvalidValue, value)
	}
}

func tlsBuiltinCAOptionsEqual(left, right api.TLSBuiltinCAOptions) bool {
	return left.Issuer == right.Issuer &&
		left.ValidityDays == right.ValidityDays &&
		left.KeyAlgorithm == right.KeyAlgorithm &&
		left.Curve == right.Curve &&
		left.KeyBits == right.KeyBits
}

func describeTLSBuiltinCAOptions(options api.TLSBuiltinCAOptions) string {
	if options.KeyAlgorithm == "ecdsa" {
		return fmt.Sprintf("issuer=%q validity_days=%d key_algorithm=%s curve=%s", options.Issuer, options.ValidityDays, options.KeyAlgorithm, options.Curve)
	}
	if options.KeyAlgorithm == "rsa" {
		return fmt.Sprintf("issuer=%q validity_days=%d key_algorithm=%s key_bits=%d", options.Issuer, options.ValidityDays, options.KeyAlgorithm, options.KeyBits)
	}
	return fmt.Sprintf("issuer=%q validity_days=%d key_algorithm=%s", options.Issuer, options.ValidityDays, options.KeyAlgorithm)
}

func describeTLSBuiltinCACert(ca *tlsBuiltinCA) string {
	if ca == nil || ca.certificate == nil {
		return "certificate=<nil>"
	}
	return fmt.Sprintf("issuer=%q not_after=%s", ca.certificate.Issuer.CommonName, ca.certificate.NotAfter.UTC().Format(time.RFC3339))
}

func loadOrCreateTLSBuiltinCA(storageDir string, options api.TLSBuiltinCAOptions, now time.Time) (*tlsBuiltinCA, error) {
	options, err := normalizeTLSBuiltinCAOptions(options)
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(storageDir, tlsBuiltinCADirName)
	golog.Debugf("builtin TLS CA: checking runtime storage at %s with %s", dir, describeTLSBuiltinCAOptions(options))
	current, state, err := readTLSBuiltinCAFromDir(dir)
	if err == nil {
		stateOptions, stateErr := normalizeTLSBuiltinCAOptions(state.Options)
		if stateErr == nil &&
			tlsBuiltinCAOptionsEqual(stateOptions, options) &&
			!tlsBuiltinCACertExpired(current.certificate, now) {
			current.options = stateOptions
			golog.Debugf("builtin TLS CA: using existing runtime certificate from %s (%s)", dir, describeTLSBuiltinCACert(current))
			return current, nil
		}
		reason := "stored options do not match configured options"
		if stateErr != nil {
			reason = fmt.Sprintf("stored options are invalid: %v", stateErr)
		} else if tlsBuiltinCACertExpired(current.certificate, now) {
			reason = fmt.Sprintf("stored certificate expired at %s", current.certificate.NotAfter.UTC().Format(time.RFC3339))
		}
		golog.Debugf("builtin TLS CA: regenerating runtime certificate because %s", reason)
	} else if !errors.Is(err, errTLSBuiltinCANotFound) && !errors.Is(err, errTLSBuiltinCAInvalid) {
		return nil, err
	} else {
		golog.Debugf("builtin TLS CA: runtime certificate will be generated because %v", err)
	}

	return generateAndStoreTLSBuiltinCA(dir, options, now)
}

func generateAndStoreTLSBuiltinCA(dir string, options api.TLSBuiltinCAOptions, now time.Time) (*tlsBuiltinCA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create builtin TLS CA directory: %w", err)
	}
	golog.Debugf("builtin TLS CA: generating certificate into %s with %s", dir, describeTLSBuiltinCAOptions(options))
	ca, err := generateTLSBuiltinCA(options, now)
	if err != nil {
		return nil, err
	}
	state := tlsBuiltinCAState{
		Version:   1,
		Options:   ca.options,
		CreatedAt: now.UTC().Format(time.RFC3339Nano),
	}
	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	stateData = append(stateData, '\n')

	if err := writeTLSBuiltinCAFile(filepath.Join(dir, tlsBuiltinCAKeyFile), []byte(ca.privateKeyPEM)); err != nil {
		return nil, err
	}
	if err := writeTLSBuiltinCAFile(filepath.Join(dir, tlsBuiltinCACertFile), []byte(ca.certPEM)); err != nil {
		return nil, err
	}
	if err := writeTLSBuiltinCAFile(filepath.Join(dir, tlsBuiltinCAStateFile), stateData); err != nil {
		return nil, err
	}
	golog.Debugf("builtin TLS CA: stored runtime certificate at %s (%s)", dir, describeTLSBuiltinCACert(ca))
	return ca, nil
}

func readTLSBuiltinCAFromDir(dir string) (*tlsBuiltinCA, tlsBuiltinCAState, error) {
	certPEM, err := readTLSBuiltinCAFile(filepath.Join(dir, tlsBuiltinCACertFile))
	if err != nil {
		return nil, tlsBuiltinCAState{}, err
	}
	keyPEM, err := readTLSBuiltinCAFile(filepath.Join(dir, tlsBuiltinCAKeyFile))
	if err != nil {
		return nil, tlsBuiltinCAState{}, err
	}
	stateData, err := readTLSBuiltinCAFile(filepath.Join(dir, tlsBuiltinCAStateFile))
	if err != nil {
		return nil, tlsBuiltinCAState{}, err
	}
	var state tlsBuiltinCAState
	if err := json.Unmarshal([]byte(stateData), &state); err != nil {
		return nil, tlsBuiltinCAState{}, fmt.Errorf("%w: decode state: %v", errTLSBuiltinCAInvalid, err)
	}
	ca, err := parseTLSBuiltinCAFiles(certPEM, keyPEM, false)
	if err != nil {
		return nil, tlsBuiltinCAState{}, err
	}
	golog.Debugf("builtin TLS CA: read runtime certificate files from %s", dir)
	return ca, state, nil
}

func readTLSBuiltinCAFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return string(data), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", errTLSBuiltinCANotFound
	}
	return "", fmt.Errorf("read builtin TLS CA file %q: %w", path, err)
}

// writeTLSBuiltinCAFile writes by rename so a crash cannot leave a partially
// written certificate, key, or state file at the final path.
func writeTLSBuiltinCAFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary builtin TLS CA file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace builtin TLS CA file: %w", err)
	}
	return nil
}

func generateTLSBuiltinCA(options api.TLSBuiltinCAOptions, now time.Time) (*tlsBuiltinCA, error) {
	options, err := normalizeTLSBuiltinCAOptions(options)
	if err != nil {
		return nil, err
	}
	signer, privateKeyPEM, err := generateTLSBuiltinCAKey(options)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate builtin TLS CA serial: %w", err)
	}

	notBefore := now.UTC().Add(-time.Hour)
	notAfter := tlsBuiltinCANoExpirationNotAfter
	if options.ValidityDays > 0 {
		notAfter = now.UTC().Add(time.Duration(options.ValidityDays) * 24 * time.Hour)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: options.Issuer,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          tlsBuiltinCASubjectKeyID(signer.Public()),
	}
	template.SignatureAlgorithm = tlsBuiltinCASignatureAlgorithm(options)

	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		return nil, fmt.Errorf("generate builtin TLS CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse generated builtin TLS CA certificate: %w", err)
	}
	return &tlsBuiltinCA{
		certPEM:       tlsPEMFromDER(der),
		privateKeyPEM: privateKeyPEM,
		signer:        signer,
		certificate:   cert,
		options:       options,
	}, nil
}

func generateTLSBuiltinCAKey(options api.TLSBuiltinCAOptions) (crypto.Signer, string, error) {
	var signer crypto.Signer
	var err error
	switch options.KeyAlgorithm {
	case "ecdsa":
		var curve elliptic.Curve
		switch options.Curve {
		case "P256":
			curve = elliptic.P256()
		case "P384":
			curve = elliptic.P384()
		case "P521":
			curve = elliptic.P521()
		default:
			return nil, "", fmt.Errorf("%w: unsupported builtin TLS CA ECDSA curve %q", almagest.ErrTlsConfigInvalidValue, options.Curve)
		}
		signer, err = ecdsa.GenerateKey(curve, rand.Reader)
	case "ed25519":
		_, key, genErr := ed25519.GenerateKey(rand.Reader)
		signer = key
		err = genErr
	case "rsa":
		signer, err = rsa.GenerateKey(rand.Reader, options.KeyBits)
	default:
		return nil, "", fmt.Errorf("%w: unsupported builtin TLS CA key algorithm %q", almagest.ErrTlsConfigInvalidValue, options.KeyAlgorithm)
	}
	if err != nil {
		return nil, "", fmt.Errorf("generate builtin TLS CA private key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, "", fmt.Errorf("marshal builtin TLS CA private key: %w", err)
	}
	return signer, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func tlsBuiltinCASignatureAlgorithm(options api.TLSBuiltinCAOptions) x509.SignatureAlgorithm {
	switch options.KeyAlgorithm {
	case "ecdsa":
		switch options.Curve {
		case "P384":
			return x509.ECDSAWithSHA384
		case "P521":
			return x509.ECDSAWithSHA512
		default:
			return x509.ECDSAWithSHA256
		}
	case "ed25519":
		return x509.PureEd25519
	case "rsa":
		if options.KeyBits >= 4096 {
			return x509.SHA512WithRSA
		}
		return x509.SHA256WithRSA
	default:
		return x509.UnknownSignatureAlgorithm
	}
}

func tlsBuiltinCASubjectKeyID(publicKey crypto.PublicKey) []byte {
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return nil
	}
	sum := sha1.Sum(der)
	return sum[:]
}

func parseProtectedTLSBuiltinCA(pemText string) (*tlsBuiltinCA, error) {
	certPEM, keyPEM, err := splitTLSBuiltinCACombinedPEM(pemText)
	if err != nil {
		return nil, err
	}
	return parseTLSBuiltinCAFiles(certPEM, keyPEM, true)
}

func parseTLSBuiltinCAFiles(certPEM, keyPEM string, immutable bool) (*tlsBuiltinCA, error) {
	_, cert, err := parseTLSAuthorityPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: certificate: %v", errTLSBuiltinCAInvalid, err)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return nil, fmt.Errorf("%w: certificate must be a CA", errTLSBuiltinCAInvalid)
	}
	if cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, fmt.Errorf("%w: certificate cannot sign certificates", errTLSBuiltinCAInvalid)
	}
	signer, err := parseTLSBuiltinCAPrivateKeyPEM(keyPEM)
	if err != nil {
		return nil, err
	}
	if !tlsBuiltinCAPublicKeysEqual(cert.PublicKey, signer.Public()) {
		return nil, fmt.Errorf("%w: certificate public key does not match private key", errTLSBuiltinCAInvalid)
	}
	return &tlsBuiltinCA{
		certPEM:       certPEM,
		privateKeyPEM: keyPEM,
		signer:        signer,
		certificate:   cert,
		options:       deriveTLSBuiltinCAOptions(cert, signer),
		immutable:     immutable,
	}, nil
}

func splitTLSBuiltinCACombinedPEM(pemText string) (string, string, error) {
	var certPEM string
	var keyPEM string
	rest := []byte(pemText)
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE":
			if certPEM != "" {
				return "", "", fmt.Errorf("%w: PEM contains more than one certificate", errTLSBuiltinCAInvalid)
			}
			certPEM = string(pem.EncodeToMemory(block))
		case tlsBuiltinCAPEMBlockIsPrivateKey(block):
			if keyPEM != "" {
				return "", "", fmt.Errorf("%w: PEM contains more than one private key", errTLSBuiltinCAInvalid)
			}
			keyPEM = string(pem.EncodeToMemory(block))
		default:
			return "", "", fmt.Errorf("%w: unsupported PEM block %q", errTLSBuiltinCAInvalid, block.Type)
		}
		rest = next
	}
	if strings.TrimSpace(string(rest)) != "" {
		return "", "", fmt.Errorf("%w: trailing non-PEM data", errTLSBuiltinCAInvalid)
	}
	if certPEM == "" || keyPEM == "" {
		return "", "", fmt.Errorf("%w: PEM must contain one certificate and one private key", errTLSBuiltinCAInvalid)
	}
	return certPEM, keyPEM, nil
}

func parseTLSBuiltinCAPrivateKeyPEM(keyPEM string) (crypto.Signer, error) {
	var keyBlock *pem.Block
	rest := []byte(keyPEM)
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		if !tlsBuiltinCAPEMBlockIsPrivateKey(block) {
			return nil, fmt.Errorf("%w: unsupported private-key PEM block %q", errTLSBuiltinCAInvalid, block.Type)
		}
		if keyBlock != nil {
			return nil, fmt.Errorf("%w: PEM contains more than one private key", errTLSBuiltinCAInvalid)
		}
		keyBlock = block
		rest = next
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("%w: trailing non-PEM private-key data", errTLSBuiltinCAInvalid)
	}
	if keyBlock == nil {
		return nil, fmt.Errorf("%w: PEM does not contain a private key", errTLSBuiltinCAInvalid)
	}

	key, err := parseTLSBuiltinCAPrivateKeyDER(keyBlock.Type, keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%w: private key type %T cannot sign certificates", errTLSBuiltinCAInvalid, key)
	}
	return signer, nil
}

func tlsBuiltinCAPEMBlockIsPrivateKey(block *pem.Block) bool {
	return block != nil && (block.Type == "PRIVATE KEY" ||
		block.Type == "EC PRIVATE KEY" ||
		block.Type == "RSA PRIVATE KEY")
}

func parseTLSBuiltinCAPrivateKeyDER(blockType string, der []byte) (any, error) {
	switch blockType {
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("%w: parse PKCS#8 private key: %v", errTLSBuiltinCAInvalid, err)
		}
		return key, nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("%w: parse EC private key: %v", errTLSBuiltinCAInvalid, err)
		}
		return key, nil
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("%w: parse RSA private key: %v", errTLSBuiltinCAInvalid, err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("%w: unsupported private-key PEM block %q", errTLSBuiltinCAInvalid, blockType)
	}
}

func tlsBuiltinCAPublicKeysEqual(left, right crypto.PublicKey) bool {
	switch l := left.(type) {
	case *ecdsa.PublicKey:
		r, ok := right.(*ecdsa.PublicKey)
		return ok && l.Curve == r.Curve && l.X.Cmp(r.X) == 0 && l.Y.Cmp(r.Y) == 0
	case ed25519.PublicKey:
		r, ok := right.(ed25519.PublicKey)
		return ok && bytes.Equal(l, r)
	case *rsa.PublicKey:
		r, ok := right.(*rsa.PublicKey)
		return ok && l.E == r.E && l.N.Cmp(r.N) == 0
	default:
		return false
	}
}

func deriveTLSBuiltinCAOptions(cert *x509.Certificate, signer crypto.Signer) api.TLSBuiltinCAOptions {
	options := api.TLSBuiltinCAOptions{
		Issuer:       strings.TrimSpace(cert.Subject.CommonName),
		ValidityDays: deriveTLSBuiltinCAValidityDays(cert),
	}
	if options.Issuer == "" {
		options.Issuer = strings.TrimSpace(cert.Subject.String())
	}
	if options.Issuer == "" {
		options.Issuer = tlsBuiltinCADefaultIssuer
	}
	switch key := signer.Public().(type) {
	case *ecdsa.PublicKey:
		options.KeyAlgorithm = "ecdsa"
		options.Curve = tlsBuiltinCACurveName(key.Curve)
	case ed25519.PublicKey:
		options.KeyAlgorithm = "ed25519"
	case *rsa.PublicKey:
		options.KeyAlgorithm = "rsa"
		options.KeyBits = key.N.BitLen()
	default:
		options.KeyAlgorithm = "unknown"
	}
	return options
}

func deriveTLSBuiltinCAValidityDays(cert *x509.Certificate) int {
	if cert.NotAfter.Year() >= tlsBuiltinCANoExpirationNotAfter.Year() {
		return 0
	}
	if !cert.NotAfter.After(cert.NotBefore) {
		return 0
	}
	days := int(math.Ceil(cert.NotAfter.Sub(cert.NotBefore).Hours() / 24))
	if days < 0 {
		return 0
	}
	return days
}

func tlsBuiltinCACurveName(curve elliptic.Curve) string {
	if curve == nil || curve.Params() == nil {
		return ""
	}
	switch curve.Params().Name {
	case "P-256":
		return "P256"
	case "P-384":
		return "P384"
	case "P-521":
		return "P521"
	default:
		return curve.Params().Name
	}
}

func tlsBuiltinCACertExpired(cert *x509.Certificate, now time.Time) bool {
	return cert != nil && !now.UTC().Before(cert.NotAfter)
}
