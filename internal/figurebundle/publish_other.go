//go:build !windows && !linux

package figurebundle

import "fmt"

func publishNewDirectory(source, target string) error {
	// Fail closed on platforms without an implemented exclusive rename. A
	// check-then-Rename fallback could destroy a concurrent empty directory.
	return fmt.Errorf("atomic non-replacing figure bundle publication is unavailable on this platform")
}
