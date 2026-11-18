//go:build !windows && !unix

package runner

import (
	"errors"
	"os/exec"
)

func configureAlgorithmCommand(cmd *exec.Cmd) {}
func prepareAlgorithmProcess(cmd *exec.Cmd) (func() error, func() error, error) {
	return nil, nil, errors.New("safe external algorithm process containment is unavailable on this platform")
}
