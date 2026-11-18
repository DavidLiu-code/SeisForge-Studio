//go:build unix

package runner

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureAlgorithmCommand(cmd *exec.Cmd) {}

func prepareAlgorithmProcess(cmd *exec.Cmd) (func() error, func() error, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cleanup := func() error {
		if cmd.Process != nil {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
		}
		return nil
	}
	return func() error { return nil }, cleanup, nil
}
