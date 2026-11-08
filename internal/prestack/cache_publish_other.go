//go:build !windows

package prestack

import (
	"context"
	"os"
)

func publishMetadataCache(ctx context.Context, source, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(source, target)
}
