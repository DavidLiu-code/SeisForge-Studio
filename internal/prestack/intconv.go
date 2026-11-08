package prestack

import (
	"fmt"
	"strconv"
)

// CheckedInt64ToInt converts a parsed 64-bit integer without truncation. The
// strconv.Atoi round-trip intentionally lets the runtime enforce the native
// int width on both 32-bit and 64-bit builds.
func CheckedInt64ToInt(value int64) (int, error) {
	converted, err := strconv.Atoi(strconv.FormatInt(value, 10))
	if err != nil {
		return 0, fmt.Errorf("integer %d does not fit in int: %w", value, err)
	}
	return converted, nil
}
