//go:build !windows

package registry

func isPackPathSpellingAlias(_, _ string) bool { return false }
