package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// -update rewrites the golden files instead of comparing against them:
//
//	go test ./cmd -run Golden -update
//
// The rendered output is the product here. A responder reads it at 3am and
// decides what to do next, so a change to its shape should be a deliberate diff
// in a review rather than something noticed later.
var update = flag.Bool("update", false, "rewrite golden files")

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("..", "testdata", "golden", name)

	if *update {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run: go test ./cmd -run Golden -update")
	require.Equal(t, string(want), got,
		"rendered output changed; if that was intended, run: go test ./cmd -run Golden -update")
}

func TestGoldenInvestigateWithoutGraph(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := executeCommand(t,
		"investigate",
		"--service", "checkout",
		"--at", "2026-01-15T12:00:00Z",
		"--window", "2h",
		"--events", filepath.Join("..", "testdata", "incident-example.json"),
		"--top", "4",
	)
	require.NoError(t, err)
	assertGolden(t, "investigate.txt", out)
}

// The same incident with a dependency graph loaded. Comparing the two files is
// the clearest statement of what the graph buys: the auth alert climbs from a
// distant third-place signal to a genuine second candidate, and it says why.
func TestGoldenInvestigateWithGraph(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out, err := executeCommand(t,
		"investigate",
		"--service", "checkout",
		"--at", "2026-01-15T12:00:00Z",
		"--window", "2h",
		"--events", filepath.Join("..", "testdata", "incident-example.json"),
		"--graph", filepath.Join("..", "testdata", "service-dependencies.json"),
		"--top", "4",
	)
	require.NoError(t, err)
	assertGolden(t, "investigate-with-graph.txt", out)
}

func TestGoldenInvestigateNoCandidates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A window that ends before any of the recorded events happened.
	out, err := executeCommand(t,
		"investigate",
		"--service", "checkout",
		"--at", "2026-01-15T09:00:00Z",
		"--window", "30m",
		"--events", filepath.Join("..", "testdata", "incident-example.json"),
	)
	require.NoError(t, err)
	assertGolden(t, "investigate-empty.txt", out)
}
