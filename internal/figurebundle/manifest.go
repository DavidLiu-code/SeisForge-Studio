// Package figurebundle stores data-dependent, reproducible figure recipes.
// It does not interpret domain payloads, execute tools, or own source Readers.
package figurebundle

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	Schema                  = "seisforge.figure.v1"
	MaxManifestBytes  int64 = 32 << 20
	MaxRasterPixels   int64 = 64_000_000
	RecipeFilename          = "recipe.json"
	ReferenceFilename       = "reference.png"
	ScriptFilename          = "render.ps1"
	ReadmeFilename          = "README.txt"
)

var (
	ErrInvalidManifest = errors.New("invalid figure manifest")
	ErrSourceChanged   = errors.New("source changed while hashing")
	ErrPixelMismatch   = errors.New("figure pixel hash mismatch")
	ErrExists          = errors.New("figure output already exists")
)

type SourceIdentity struct {
	Path             string          `json:"path"`
	Size             int64           `json:"size"`
	ModifiedUnixNano int64           `json:"modified_unix_nano"`
	SHA256           string          `json:"sha256"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type BundleManifest struct {
	Schema             string          `json:"schema"`
	RendererSHA256     string          `json:"renderer_sha256"`
	ApplicationVersion string          `json:"application_version"`
	SourceA            SourceIdentity  `json:"source_a"`
	SourceB            *SourceIdentity `json:"source_b,omitempty"`
	Payload            json.RawMessage `json:"payload"`
	PixelSHA256        string          `json:"pixel_sha256"`
	Width              int             `json:"width"`
	Height             int             `json:"height"`
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validDimensions(width, height int) bool {
	return width > 0 && height > 0 && int64(width) <= MaxRasterPixels && int64(height) <= MaxRasterPixels/int64(width)
}

func (m BundleManifest) Validate() error {
	bad := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidManifest, reason) }
	if m.Schema != Schema {
		return bad("unknown schema")
	}
	if strings.TrimSpace(m.ApplicationVersion) == "" {
		return bad("missing application version")
	}
	if !validSHA256(m.RendererSHA256) || !validSHA256(m.PixelSHA256) {
		return bad("invalid renderer/pixel SHA-256")
	}
	if !validDimensions(m.Width, m.Height) {
		return bad("raster exceeds 64 megapixels or has invalid dimensions")
	}
	if len(m.Payload) == 0 || int64(len(m.Payload)) > MaxManifestBytes || !json.Valid(m.Payload) || bytes.Equal(bytes.TrimSpace(m.Payload), []byte("null")) {
		return bad("missing or invalid numeric-parameter payload")
	}
	for _, source := range []*SourceIdentity{&m.SourceA, m.SourceB} {
		if source == nil {
			continue
		}
		if strings.TrimSpace(source.Path) == "" || strings.ContainsRune(source.Path, 0) || source.Size < 0 || !validSHA256(source.SHA256) {
			return bad("invalid source identity")
		}
		if len(source.Metadata) > 0 && !json.Valid(source.Metadata) {
			return bad("invalid source metadata JSON")
		}
	}
	return nil
}

// DecodeManifest limits the complete input, including trailing whitespace,
// before decoding. Unknown envelope fields and additional JSON values fail.
func DecodeManifest(input io.Reader) (BundleManifest, error) {
	var m BundleManifest
	if input == nil {
		return m, fmt.Errorf("%w: nil input", ErrInvalidManifest)
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxManifestBytes+1))
	if err != nil {
		return m, err
	}
	if int64(len(data)) > MaxManifestBytes {
		return m, fmt.Errorf("%w: JSON exceeds 32 MiB", ErrInvalidManifest)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return BundleManifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return BundleManifest{}, fmt.Errorf("%w: trailing JSON", ErrInvalidManifest)
	}
	if err := m.Validate(); err != nil {
		return BundleManifest{}, err
	}
	return m, nil
}

func ReadManifest(path string) (BundleManifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return BundleManifest{}, err
	}
	defer f.Close()
	return DecodeManifest(f)
}
