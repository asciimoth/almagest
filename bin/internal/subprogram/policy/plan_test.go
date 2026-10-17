package policy

import (
	"errors"
	"testing"

	sandbox "github.com/asciimoth/gobox"
)

func fullProbe(platform string) Probe {
	p := Probe{Platform: platform, EffectiveIdentity: "test-user", Available: map[sandbox.Feature]bool{}}
	for _, f := range features {
		p.Available[f] = true
	}
	return p
}
func TestStrictProfiles(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		p := fullProbe(platform)
		s, err := Select(sandbox.Strict, p, nil)
		if err != nil || !s.StrictSatisfied || !s.FullBoundary {
			t.Fatalf("%s: %+v %v", platform, s, err)
		}
		delete(p.Available, sandbox.PrivateNetwork)
		s, err = Select(sandbox.Strict, p, nil)
		if platform == "linux" {
			if !errors.Is(err, sandbox.ErrUnavailable) {
				t.Fatalf("Linux exclusion: %v", err)
			}
		} else {
			if err != nil || !s.StrictSatisfied || s.FullBoundary {
				t.Fatalf("Windows exclusion: %+v %v", s, err)
			}
			if _, err = Select(sandbox.Strict, p, []sandbox.Feature{sandbox.PrivateNetwork}); !errors.Is(err, sandbox.ErrUnavailable) {
				t.Fatalf("required feature waived: %v", err)
			}
		}
		delete(p.Available, sandbox.ProcessTree)
		if _, err = Select(sandbox.Strict, p, nil); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Fatalf("missing required process tree: %v", err)
		}
	}
	if _, err := Select(sandbox.Strict, fullProbe("unknown"), nil); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestAutoAndOffPlans(t *testing.T) {
	p := Probe{Platform: "linux", EffectiveIdentity: "root", Available: map[sandbox.Feature]bool{sandbox.Environment: true}}
	s, err := Select(sandbox.Auto, p, nil)
	if err != nil || s.StrictSatisfied || s.FullBoundary || len(s.Warnings) < len(features) {
		t.Fatalf("partial auto: %+v %v", s, err)
	}
	for _, f := range s.Features {
		if f.Feature != sandbox.Environment && f.State != sandbox.OmittedAuto {
			t.Fatal(f)
		}
	}
	for _, mode := range []sandbox.Mode{sandbox.Auto, sandbox.Off} {
		if _, err = Select(mode, p, []sandbox.Feature{sandbox.PrivateNetwork}); err == nil {
			t.Fatal("ignored required feature")
		}
	}
	s, err = Select(sandbox.Off, fullProbe("linux"), nil)
	if err != nil || s.FullBoundary || s.StrictSatisfied {
		t.Fatal(s, err)
	}
	for _, f := range s.Features {
		if f.State != sandbox.Disabled {
			t.Fatal(f)
		}
	}
}
func TestPlanVerification(t *testing.T) {
	s, err := Select(sandbox.Strict, fullProbe("linux"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(s, features); err != nil {
		t.Fatal(err)
	}
	if Verify(s, features[:len(features)-1]) == nil || Verify(s, append(append([]sandbox.Feature{}, features...), sandbox.Environment)) == nil {
		t.Fatal("accepted partial or duplicate verification")
	}
	for _, required := range [][]sandbox.Feature{{"made-up"}, {sandbox.Environment, sandbox.Environment}} {
		if _, err = Select(sandbox.Auto, fullProbe("linux"), required); err == nil {
			t.Fatal("invalid feature accepted")
		}
	}
}

func TestProbeStatusDoesNotExposeNativeDetails(t *testing.T) {
	p := fullProbe("linux")
	p.Reasons = map[sandbox.Feature]string{sandbox.PrivateNetwork: "/secret/host/path"}
	if _, err := Select(sandbox.Auto, p, nil); !errors.Is(err, sandbox.ErrInvalid) {
		t.Fatal(err)
	}
	p.Reasons = nil
	p.EffectiveIdentity = "user\nforged status"
	if _, err := Select(sandbox.Auto, p, nil); !errors.Is(err, sandbox.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestVerificationRejectsMalformedPlan(t *testing.T) {
	if Verify(sandbox.Status{}, nil) == nil {
		t.Fatal("empty plan verified")
	}
	plan, err := Select(sandbox.Strict, fullProbe("linux"), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.Features[0].State = sandbox.ExcludedOS
	if Verify(plan, features[1:]) == nil {
		t.Fatal("Linux OS exclusion verified")
	}
	plan.Features[0].State = sandbox.OmittedAuto
	if Verify(plan, features[1:]) == nil {
		t.Fatal("strict omission verified")
	}
}
