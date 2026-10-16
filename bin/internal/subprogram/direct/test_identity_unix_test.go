//go:build unix

package direct

import (
	"fmt"
	"os"
)

func helperIdentity() string { return fmt.Sprintf("%d:%d", os.Geteuid(), os.Getegid()) }
