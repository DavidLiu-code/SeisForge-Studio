// Package provenance stores metadata-only algorithm run records outside source
// files and never stores a Reader, samples, executable bytes or credentials.
package provenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func DefaultDirectory() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Limage", "algorithm_runs", "v1"), nil
}

// Save publishes a new immutable record. Existing run IDs are never overwritten.
func Save(directory string, record api.AlgorithmRunRecord) (string, error) {
	if record.Schema != api.RunRecordSchema || !safeID.MatchString(record.RunID) {
		return "", errors.New("invalid algorithm run record identity")
	}
	if directory == "" {
		var err error
		directory, err = DefaultDirectory()
		if err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, record.RunID+".json")
	f, err := os.CreateTemp(directory, ".run-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err = enc.Encode(record); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	// Link is an atomic create-if-absent publication on Windows and Unix. It
	// avoids os.Rename's platform-dependent overwrite behavior.
	if err = os.Link(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}
