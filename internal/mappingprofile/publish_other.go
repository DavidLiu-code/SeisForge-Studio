//go:build !windows

package mappingprofile

import (
	"fmt"
	"os"
)

func publishProfile(source, target string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(target); err == nil {
			return ErrProfileExists
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if overwrite {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("publish mapping profile: %w", err)
	}
	return nil
}
