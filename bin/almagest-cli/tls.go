package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/asciimoth/almagest/bin/api"
)

func runTLS(ctx context.Context, c *client, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printTLSHelp(stdout)
		return nil
	}
	switch args[0] {
	case "list":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceTLS, string(api.MethodTLSSnapshot), nil, stdout, stderr)
	case "crls":
		if err := parseNoArgs(args[0], args[1:], stderr); err != nil {
			return err
		}
		raw, err := c.request(ctx, api.SurfaceTLS, string(api.MethodTLSSnapshot), nil)
		if err != nil {
			return err
		}
		var snapshot api.TLSSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		return writePrettyJSON(stdout, snapshot.Revocations)
	case "watch":
		if err := parseNoArgs(args[0], args[1:], stderr); err != nil {
			return err
		}
		return subscribeAndWatchTLS(ctx, c, stdout)
	case "builtin-ca":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceTLS, string(api.MethodTLSBuiltinCA), nil, stdout, stderr)
	case "builtin-crl":
		return exportTLSBuiltinCRL(ctx, c, args[1:], stdout, stderr)
	case "set-builtin-ca":
		req, err := parseTLSSetBuiltinCARequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSSetBuiltinCA), req, stdout)
	case "reload-os-certs":
		return parseNoArgsAndRequest(ctx, c, args[1:], api.SurfaceTLS, string(api.MethodTLSReloadOSCerts), nil, stdout, stderr)
	case "set-cert":
		req, err := parseTLSSetCertRequest(args[0], args[1:], stdin, stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSSetCert), req, stdout)
	case "rename-cert":
		req, err := parseTLSRenameRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSRenameCert), req, stdout)
	case "delete-cert":
		req, err := parseTLSNameRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSDeleteCert), req, stdout)
	case "set-policy":
		req, err := parseTLSSetPolicyRequest(args[0], args[1:], stdin, stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSSetPolicy), req, stdout)
	case "rename-policy":
		req, err := parseTLSRenameRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSRenamePolicy), req, stdout)
	case "delete-policy":
		req, err := parseTLSNameRequest(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSDeletePolicy), req, stdout)
	case "unsubscribe":
		fs := commandFlagSet("tls unsubscribe", stderr)
		id := fs.Uint64("subscription-id", 0, "TLS subscription ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == 0 {
			return fmt.Errorf("--subscription-id is required")
		}
		return requestPretty(ctx, c, api.SurfaceTLS, string(api.MethodTLSUnsubscribe), api.TLSSubscriptionRequest{SubscriptionID: *id}, stdout)
	default:
		return fmt.Errorf("unknown tls command %q", args[0])
	}
}

func exportTLSBuiltinCRL(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	fs := commandFlagSet("tls builtin-crl", stderr)
	out := fs.String("out", "-", "output CRL PEM path; use - for stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := c.request(ctx, api.SurfaceTLS, string(api.MethodTLSBuiltinCRL), nil)
	if err != nil {
		return err
	}
	var info api.TLSCRLInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return fmt.Errorf("decode built-in CA CRL: %w", err)
	}
	if info.PEM == "" {
		return fmt.Errorf("built-in CA CRL is empty")
	}
	if *out == "-" {
		_, err = io.WriteString(stdout, info.PEM)
		return err
	}
	if *out == "" {
		return fmt.Errorf("--out cannot be empty")
	}
	if err := os.WriteFile(*out, []byte(info.PEM), 0o644); err != nil {
		return fmt.Errorf("write built-in CA CRL: %w", err)
	}
	return nil
}

// parseTLSNameRequest builds the name-only payload shared by TLS deletes.
func parseTLSNameRequest(name string, args []string, stderr io.Writer) (api.TLSNameRequest, error) {
	fs := commandFlagSet("tls "+name, stderr)
	tlsName := fs.String("name", "", "TLS certificate or policy name")
	if err := fs.Parse(args); err != nil {
		return api.TLSNameRequest{}, err
	}
	if *tlsName == "" {
		return api.TLSNameRequest{}, fmt.Errorf("--name is required")
	}
	return api.TLSNameRequest{Name: *tlsName}, nil
}

// parseTLSRenameRequest builds the rename payload shared by certificates and
// policies. The daemon decides if the selected name is mutable.
func parseTLSRenameRequest(name string, args []string, stderr io.Writer) (api.TLSRenameRequest, error) {
	fs := commandFlagSet("tls "+name, stderr)
	tlsName := fs.String("name", "", "current TLS certificate or policy name")
	newName := fs.String("new-name", "", "new TLS certificate or policy name")
	if err := fs.Parse(args); err != nil {
		return api.TLSRenameRequest{}, err
	}
	if *tlsName == "" {
		return api.TLSRenameRequest{}, fmt.Errorf("--name is required")
	}
	if *newName == "" {
		return api.TLSRenameRequest{}, fmt.Errorf("--new-name is required")
	}
	return api.TLSRenameRequest{Name: *tlsName, NewName: *newName}, nil
}

// parseTLSSetCertRequest accepts pasted PEM text or a file path. Use
// --pem-file - when the PEM must be read from stdin.
func parseTLSSetCertRequest(name string, args []string, stdin io.Reader, stderr io.Writer) (api.TLSSetCertRequest, error) {
	fs := commandFlagSet("tls "+name, stderr)
	tlsName := fs.String("name", "", "TLS certificate name")
	pemText := fs.String("pem", "", "certificate authority PEM text")
	pemFile := fs.String("pem-file", "", "path to certificate authority PEM; use - for stdin")
	if err := fs.Parse(args); err != nil {
		return api.TLSSetCertRequest{}, err
	}
	if *tlsName == "" {
		return api.TLSSetCertRequest{}, fmt.Errorf("--name is required")
	}
	pem, err := readTLSInput(*pemText, *pemFile, stdin, "--pem", "--pem-file")
	if err != nil {
		return api.TLSSetCertRequest{}, err
	}
	return api.TLSSetCertRequest{Name: *tlsName, PEM: pem}, nil
}

// parseTLSSetPolicyRequest accepts the policy JSON shape used by the TLS API:
// trust_anchors, constraints, and pins. Use --spec-file - to read from stdin.
func parseTLSSetPolicyRequest(name string, args []string, stdin io.Reader, stderr io.Writer) (api.TLSSetPolicyRequest, error) {
	fs := commandFlagSet("tls "+name, stderr)
	tlsName := fs.String("name", "", "TLS policy name")
	specText := fs.String("spec", "", "TLS policy spec JSON")
	specFile := fs.String("spec-file", "", "path to TLS policy spec JSON; use - for stdin")
	if err := fs.Parse(args); err != nil {
		return api.TLSSetPolicyRequest{}, err
	}
	if *tlsName == "" {
		return api.TLSSetPolicyRequest{}, fmt.Errorf("--name is required")
	}
	text, err := readTLSInput(*specText, *specFile, stdin, "--spec", "--spec-file")
	if err != nil {
		return api.TLSSetPolicyRequest{}, err
	}
	var spec api.TLSPolicySpec
	if err := json.Unmarshal([]byte(text), &spec); err != nil {
		return api.TLSSetPolicyRequest{}, fmt.Errorf("--spec must be valid TLS policy JSON: %w", err)
	}
	return api.TLSSetPolicyRequest{Name: *tlsName, Spec: spec}, nil
}

func parseTLSSetBuiltinCARequest(name string, args []string, stderr io.Writer) (api.TLSSetBuiltinCARequest, error) {
	fs := commandFlagSet("tls "+name, stderr)
	issuer := fs.String("issuer", "", "built-in CA issuer")
	validityDays := fs.Int("validity-days", 0, "built-in CA validity in days; 0 means no expiration")
	keyAlgorithm := fs.String("key-algorithm", "", "built-in CA key algorithm: ecdsa, ed25519, or rsa")
	curve := fs.String("curve", "", "ECDSA curve: P256, P384, or P521")
	keyBits := fs.Int("key-bits", 0, "RSA key size in bits")
	if err := fs.Parse(args); err != nil {
		return api.TLSSetBuiltinCARequest{}, err
	}
	return api.TLSSetBuiltinCARequest{
		Options: api.TLSBuiltinCAOptions{
			Issuer:       *issuer,
			ValidityDays: *validityDays,
			KeyAlgorithm: *keyAlgorithm,
			Curve:        *curve,
			KeyBits:      *keyBits,
		},
	}, nil
}

// readTLSInput reads one CLI text payload from either a direct flag or a file.
// A file value of "-" reads stdin; this avoids accidentally blocking commands
// that forgot to pass input.
func readTLSInput(value, file string, stdin io.Reader, valueFlag, fileFlag string) (string, error) {
	if value != "" && file != "" {
		return "", fmt.Errorf("%s and %s cannot be used together", valueFlag, fileFlag)
	}
	switch {
	case value != "":
		return value, nil
	case file == "-":
		if stdin == nil {
			return "", fmt.Errorf("%s - requires stdin", fileFlag)
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("%s or %s is required", valueFlag, fileFlag)
	}
}

func subscribeAndWatchTLS(ctx context.Context, c *client, stdout io.Writer) error {
	raw, err := c.request(ctx, api.SurfaceTLS, string(api.MethodTLSSubscribe), nil)
	if err != nil {
		return err
	}
	var value any
	if len(raw) == 0 {
		value = map[string]any{}
	} else if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if err := writePrettyJSON(stdout, value); err != nil {
		return err
	}
	return watchEvents(ctx, c, stdout, func(msg api.Message) bool {
		return msg.Surface == api.SurfaceTLS && msg.Method == string(api.EventTLSUpdate)
	})
}

func printTLSHelp(w io.Writer) {
	_, _ = fmt.Fprint(w, `Usage:
  almagest-cli tls <command> [flags]

Snapshot commands:
  list             list effective TLS certificates and policies
  crls             show static CRL enablement and read-only source status
  builtin-ca       show built-in TLS CA certificate and options
  builtin-crl      write built-in CA CRL PEM to stdout or --out PATH

Subscription commands:
  watch            subscribe and print TLS snapshots and updates
  unsubscribe      unsubscribe by --subscription-id

Mutation commands:
  set-builtin-ca   update built-in CA generation options
  reload-os-certs  reload read-only TLS CAs from the OS trust store
  set-cert         set --name to certificate PEM from --pem or --pem-file
  rename-cert      rename certificate --name to --new-name
  delete-cert      delete one mutable certificate by --name
  set-policy       set --name to policy JSON from --spec or --spec-file
  rename-policy    rename policy --name to --new-name
  delete-policy    delete one mutable policy by --name

Common flags:
  --name NAME      TLS certificate or policy name
  --new-name NAME  new TLS certificate or policy name for rename commands

Certificate flags for set-cert:
  --pem PEM        certificate authority PEM text
  --pem-file PATH  file containing certificate authority PEM; use - for stdin

Policy flags for set-policy:
  --spec JSON      TLS policy JSON with trust_anchors, constraints, and pins
  --spec-file PATH file containing TLS policy JSON; use - for stdin

Built-in CA flags for set-builtin-ca:
  --issuer NAME          certificate issuer (default Almagest)
  --validity-days DAYS   validity period; 0 means no expiration
  --key-algorithm ALG    ecdsa, ed25519, or rsa
  --curve CURVE          ECDSA curve: P256, P384, or P521
  --key-bits BITS        RSA key size in bits

Built-in CRL flags for builtin-crl:
  --out PATH             output CRL PEM path; use - for stdout (default -)
`)
}
