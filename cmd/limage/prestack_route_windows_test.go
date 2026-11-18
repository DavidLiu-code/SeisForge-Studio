//go:build windows

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DavidLiu-code/SeisForge-Studio/internal/workspace"
)

func TestPrestackHomeModeAndRecentCompatibility(t *testing.T) {
	if workspaceModePrestack != 4 || phase1WorkspaceKind(4) != workspace.KindPrestack || phase1LegacyMode(workspace.KindPrestack) != 4 {
		t.Fatal("Prestack must use its own persisted mode 4")
	}
	var state homeStateFile
	if err := json.Unmarshal([]byte(`{"version":1,"last_workspace":4,"recent":[{"path":"a.sgy","mode":1},{"path":"b.sgy","mode":2},{"path":"c.sgy","mode":3},{"path":"d.sgy","mode":4}]}`), &state); err != nil {
		t.Fatal(err)
	}
	for i, recent := range state.Recent {
		if recent.Mode != i+1 {
			t.Fatal("new Recent mode changed an existing mode")
		}
	}
	if workspaceModeName(4) != "叠前" {
		t.Fatal("Recent mode 4 is not labelled Prestack")
	}
}

func TestPrestackHomeCardResponsiveLayout(t *testing.T) {
	wide := computeStartHomeLayoutForSize(1400, 900, 5)
	narrow := computeStartHomeLayoutForSize(1000, 900, 5)
	if len(wide.cards) != 4 || wide.cards[0].Top != wide.cards[3].Top {
		t.Fatal("wide Home must show four independent cards")
	}
	if narrow.cards[0].Top != narrow.cards[1].Top || narrow.cards[2].Top <= narrow.cards[0].Bottom || narrow.cards[2].Top != narrow.cards[3].Top {
		t.Fatal("narrow Home must use a two-by-two card layout")
	}
	for _, layout := range []homeLayout{wide, narrow} {
		for _, card := range layout.cards {
			if card.Right <= card.Left || card.Bottom <= card.Top || card.Bottom >= layout.drop.Top {
				t.Fatal("card geometry is degenerate or overlaps the drop zone")
			}
		}
	}
	if startHomeDropHit < startHomeCardCount || startHomeClearHit == startHomeDropHit {
		t.Fatal("Prestack card collides with a Home action hit ID")
	}
}

func TestPrestackRouterRegistrationAndDedicatedDrop(t *testing.T) {
	initSource := phase1FunctionSource(t, "app_phase1_windows.go", "initializePhase1Application")
	if !strings.Contains(initSource, "&prestackWorkspaceAdapter{}") {
		t.Fatal("Prestack workspace is not independently registered")
	}
	dropSource := phase1FunctionSource(t, "workspace_windows.go", "handleWorkspaceDropPaths")
	if !strings.Contains(dropSource, "targetMode == workspaceModePrestack") || !strings.Contains(dropSource, "openApplicationPath(paths[0], workspaceModePrestack)") {
		t.Fatal("Prestack drops do not enter their dedicated application route")
	}
}
