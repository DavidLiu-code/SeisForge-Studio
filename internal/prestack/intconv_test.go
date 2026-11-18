package prestack

import (
	"strconv"
	"testing"
)

func TestCheckedInt64ToIntRejectsNarrowing(t *testing.T) {
	for _, value := range []int64{0, 1, -1} {
		if _, err := CheckedInt64ToInt(value); err != nil {
			t.Fatalf("value %d unexpectedly rejected: %v", value, err)
		}
	}
	maxInt64 := int64(^uint64(0) >> 1)
	minInt64 := -maxInt64 - 1
	_, maxErr := CheckedInt64ToInt(maxInt64)
	_, minErr := CheckedInt64ToInt(minInt64)
	if strconv.IntSize == 64 {
		if maxErr != nil || minErr != nil {
			t.Fatalf("64-bit boundaries rejected: max=%v min=%v", maxErr, minErr)
		}
	} else if maxErr == nil || minErr == nil {
		t.Fatalf("32-bit narrowing was accepted: max=%v min=%v", maxErr, minErr)
	}
}
