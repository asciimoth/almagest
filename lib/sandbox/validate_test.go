package sandbox

import (
	"errors"
	"testing"
)

func TestPortablePaths(t *testing.T) {
	for _, name := range []string{"../x", "a/../b", "/x", "C:/x", `a\b`, "a//b", "a/./b", "a/", "a:", "NUL.txt", "com1", "COM¹", "lpt9.ext", "foo.", "foo ", "a\x00b", "a\nb", "CONIN$"} {
		if err := ValidatePath(Path{Rel: name}, true); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %q: %v", name, err)
		}
	}
	for _, name := range []string{"", ".", "a", "a/b.txt", "commander", "雪/a"} {
		if err := ValidatePath(Path{Rel: name}, true); err != nil {
			t.Errorf("rejected %q: %v", name, err)
		}
	}
	if ValidatePath(Path{}, false) == nil || ValidatePath(Path{Root: 9, Rel: "a"}, true) == nil {
		t.Fatal("accepted root mutation or unknown root")
	}
}
func TestLoopbackEndpoints(t *testing.T) {
	for _, address := range []string{"localhost:80", "0.0.0.0:80", "[::]:80", "[::ffff:127.0.0.1]:80", "[::1%eth0]:80", "192.0.2.1:80", "127.0.0.1:0", "127.1:80"} {
		if err := ValidateEndpoint("tcp", address, false); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:80", "[::1]:80"} {
		if err := ValidateEndpoint("tcp", address, false); err != nil {
			t.Error(err)
		}
	}
	if ValidateEndpoint("udp4", "127.0.0.1:0", true) != nil || ValidateEndpoint("tcp4", "[::1]:80", true) == nil || ValidateEndpoint("unix", "127.0.0.1:80", true) == nil {
		t.Fatal("incorrect protocol validation")
	}
}
func TestTypedInputs(t *testing.T) {
	valid := []Value{{Kind: TextValue, Text: "hello"}, {Kind: IntegerValue, Int: -1}, {Kind: BooleanValue, Bool: true}, {Kind: PathValue, Path: Path{Persistent, "state"}}, {Kind: ProgramValue, Program: "tor"}, {Kind: EndpointValue, Endpoint: "127.0.0.1:9050"}}
	for _, v := range valid {
		if err := v.Validate(); err != nil {
			t.Errorf("%+v: %v", v, err)
		}
	}
	invalid := []Value{{}, {Kind: TextValue, Int: 1}, {Kind: IntegerValue, Bool: true}, {Kind: ProgramValue, Program: "/bin/sh"}, {Kind: PathValue, Path: Path{Rel: "../x"}}, {Kind: TextValue, Text: "a\x00b"}}
	for _, v := range invalid {
		if v.Validate() == nil {
			t.Errorf("accepted %+v", v)
		}
	}
}
func TestOpenOptions(t *testing.T) {
	for _, o := range []OpenOptions{{}, {Read: true, Create: true}, {Write: true, Exclusive: true}, {Write: true, Append: true, Truncate: true}, {Write: true, Mode: 04000}} {
		if o.Validate() == nil {
			t.Errorf("accepted %+v", o)
		}
	}
	if (OpenOptions{Read: true, Write: true, Create: true, Exclusive: true, Mode: 0600}).Validate() != nil {
		t.Fatal("valid options rejected")
	}
}
func FuzzPortablePaths(f *testing.F) {
	for _, s := range []string{"a/b", "../x", "NUL", "[::1]:80", "a\x00b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = ValidatePath(Path{Rel: s}, true)
		_ = ValidateEndpoint("tcp", s, false)
		_ = ValidateName(s)
	})
}

func FuzzTypedInputs(f *testing.F) {
	f.Add(uint8(0), "hello", int64(0), false, uint8(0), "", "", "")
	f.Add(uint8(3), "", int64(0), false, uint8(Persistent), "state", "", "")
	f.Add(uint8(5), "", int64(0), false, uint8(0), "", "", "127.0.0.1:9050")
	f.Fuzz(func(t *testing.T, kind uint8, text string, integer int64, boolean bool, root uint8, rel, program, endpoint string) {
		kinds := [...]ValueKind{TextValue, IntegerValue, BooleanValue, PathValue, ProgramValue, EndpointValue, ValueKind("unknown")}
		value := Value{
			Kind: kinds[int(kind)%len(kinds)], Text: text, Int: integer, Bool: boolean,
			Path: Path{Root: Root(root), Rel: rel}, Program: ProgramID(program), Endpoint: endpoint,
		}
		_ = value.Validate()
	})
}
