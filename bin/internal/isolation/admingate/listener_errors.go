package admingate

import "fmt"

func errorsUnsupported(name string) error {
	return fmt.Errorf("%s are not supported on this platform", name)
}
