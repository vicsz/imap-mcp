//go:build !darwin || !cgo

package observability

import (
	"fmt"
	"os"
)

func writePlatformEvent(_ string, _ bool, message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
}
