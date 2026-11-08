//go:build !windows

package prestacksession

import (
	"fmt"
	"os"
)

func publishSnapshot(source, target string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(target); err == nil {
			return ErrSnapshotExists
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
		return fmt.Errorf("publish prestack session: %w", err)
	}
	return nil
}
