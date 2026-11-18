//go:build !windows

package prestackbatch

import (
	"fmt"
	"os"
	"path/filepath"
)

func publishPlan(temp, target string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(target); err == nil {
			return ErrPlanExists
		}
	}
	if err := os.Rename(temp, target); err != nil {
		return fmt.Errorf("publish batch plan %s: %w", filepath.Base(target), err)
	}
	return nil
}
