package args

import (
	"bytes"
	"testing"

	"github.com/alexflint/go-arg"
)

func TestNoDNSCacheExpirationFlag(t *testing.T) {
	var a Args
	parser, err := arg.NewParser(arg.Config{IgnoreEnv: true, Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse([]string{"--no-dns-cache-expiration"}); err != nil {
		t.Fatalf("Parse(--no-dns-cache-expiration): %v", err)
	}
	if !a.NoDNSCacheExpiration {
		t.Fatal("NoDNSCacheExpiration = false, want true")
	}
}

func TestNoDNSCacheExpirationEnv(t *testing.T) {
	t.Setenv("ALMAGEST_NO_DNS_CACHE_EXPIRATION", "true")

	var a Args
	parser, err := arg.NewParser(arg.Config{Out: new(bytes.Buffer)}, &a)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.Parse(nil); err != nil {
		t.Fatalf("Parse with ALMAGEST_NO_DNS_CACHE_EXPIRATION: %v", err)
	}
	if !a.NoDNSCacheExpiration {
		t.Fatal("NoDNSCacheExpiration = false, want true from environment")
	}
}
