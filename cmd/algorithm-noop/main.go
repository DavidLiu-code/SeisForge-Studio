// algorithm-noop is a public reference implementation of the external algorithm
// protocol. It copies a validated artifact without changing samples or geometry.
// It is not installed automatically and is never an unknown-algorithm fallback.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	api "github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi"
	"github.com/DavidLiu-code/SeisForge-Studio/pkg/algorithmapi/artifact"
)

func descriptor() api.AlgorithmDescriptor {
	return api.AlgorithmDescriptor{ID: "demo.noop", Version: "1.0.0", Name: "Noop / Copy (public example)", Category: "example", Description: "Copies a validated sfdata artifact without modifying any samples.", ProtocolVersion: api.ProtocolVersion, InputKinds: []string{"section2d", "volume3d"}, OutputKinds: []string{"section2d", "volume3d"}, SupportsProgress: true, SupportsCancel: true, Parameters: []api.ParameterDescriptor{}, Executable: "algorithm-noop.exe", MinimumHostVersion: "1.10.4"}
}

func run() error {
	describe := flag.Bool("describe", false, "describe the public example")
	execute := flag.Bool("run", false, "execute a request")
	requestPath := flag.String("request", "", "metadata-only JSON request")
	inputPath := flag.String("input", "", "input artifact")
	outputPath := flag.String("output", "", "output artifact")
	flag.Parse()
	enc := json.NewEncoder(os.Stdout)
	if *describe && !(*execute) {
		return enc.Encode(descriptor())
	}
	if !*execute || *describe || *requestPath == "" || *inputPath == "" || *outputPath == "" {
		return errors.New("use --describe or --run --request request.json --input input.sfdata --output output.sfdata")
	}
	rf, err := os.Open(*requestPath)
	if err != nil {
		return err
	}
	var request api.RunRequest
	dec := json.NewDecoder(io.LimitReader(rf, 1<<20))
	dec.DisallowUnknownFields()
	err = dec.Decode(&request)
	rf.Close()
	if err != nil {
		return err
	}
	desc := descriptor()
	if request.ProtocolVersion != api.ProtocolVersion || request.AlgorithmID != desc.ID || request.AlgorithmVersion != desc.Version {
		return errors.New("incompatible noop request")
	}
	if _, err = api.NormalizeParameters(desc.Parameters, request.Parameters); err != nil {
		return err
	}
	input, err := artifact.OpenContext(context.Background(), *inputPath)
	if err != nil {
		return err
	}
	fingerprint := input.Fingerprint()
	input.Close()
	if fingerprint != request.InputFingerprint {
		return errors.New("input fingerprint mismatch")
	}
	if err = enc.Encode(api.ProgressEvent{Type: "started"}); err != nil {
		return err
	}
	src, err := os.Open(*inputPath)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(*outputPath), ".noop-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	_, err = io.Copy(tmp, src)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(name, *outputPath); err != nil {
		return err
	}
	if err = enc.Encode(api.ProgressEvent{Type: "progress", Value: 1, Message: "Artifact copied without changing samples or geometry"}); err != nil {
		return err
	}
	return enc.Encode(api.ProgressEvent{Type: "result", Status: "ok", Output: *outputPath})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
