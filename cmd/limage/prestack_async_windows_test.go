//go:build windows

package main

import "testing"

func TestPrestackAsyncSourceTokenRejectsReopenedWindow(t *testing.T) {
	a := prestackAsyncToken{owner: 7, ownerToken: 11, datasetGeneration: 3, workspaceGeneration: 9}
	if !prestackAsyncSourceMatches(a, a) {
		t.Fatal("identical async source token was rejected")
	}
	for name, b := range map[string]prestackAsyncToken{
		"owner":           {owner: 8, ownerToken: 11, datasetGeneration: 3, workspaceGeneration: 9},
		"reopened-window": {owner: 7, ownerToken: 12, datasetGeneration: 3, workspaceGeneration: 9},
		"dataset":         {owner: 7, ownerToken: 11, datasetGeneration: 4, workspaceGeneration: 9},
		"workspace":       {owner: 7, ownerToken: 11, datasetGeneration: 3, workspaceGeneration: 10},
	} {
		if prestackAsyncSourceMatches(a, b) {
			t.Fatalf("stale %s token was accepted", name)
		}
	}
}

func TestPrestackAsyncRenderTokenRejectsSelectionAndResize(t *testing.T) {
	a := prestackAsyncToken{owner: 7, ownerToken: 11, datasetGeneration: 3, selectionGeneration: 4, sizeGeneration: 5, workspaceGeneration: 9, page: 0}
	if !prestackAsyncRenderMatches(a, a) {
		t.Fatal("identical render token was rejected")
	}
	for name, b := range map[string]prestackAsyncToken{
		"selection": {owner: 7, ownerToken: 11, datasetGeneration: 3, selectionGeneration: 6, sizeGeneration: 5, workspaceGeneration: 9, page: 0},
		"resize":    {owner: 7, ownerToken: 11, datasetGeneration: 3, selectionGeneration: 4, sizeGeneration: 6, workspaceGeneration: 9, page: 0},
		"mapping":   {owner: 7, ownerToken: 11, datasetGeneration: 3, selectionGeneration: 4, sizeGeneration: 5, workspaceGeneration: 9, page: 1},
	} {
		if prestackAsyncRenderMatches(a, b) {
			t.Fatalf("stale %s render token was accepted", name)
		}
	}
}

func TestPrestackRenderBufferValidation(t *testing.T) {
	valid := &prestackRenderResult{width: 2, height: 3, indices: make([]byte, 6), bgra: make([]byte, 24)}
	if !prestackValidRenderBuffers(valid) {
		t.Fatal("valid render buffers rejected")
	}
	for _, bad := range []*prestackRenderResult{
		{width: 0, height: 3, indices: make([]byte, 6), bgra: make([]byte, 24)},
		{width: 2, height: 3, indices: make([]byte, 5), bgra: make([]byte, 24)},
		{width: 2, height: 3, indices: make([]byte, 6), bgra: make([]byte, 23)},
	} {
		if prestackValidRenderBuffers(bad) {
			t.Fatal("invalid render buffers accepted")
		}
	}
}
