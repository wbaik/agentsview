package parser

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
)

// TestExtractProjectFromCwd_ForeignPath_SkipsStatWalk verifies that
// a cwd falling inside a locally-configured autofs prefix does not
// trigger any filesystem stat calls. On macOS, statting under /home
// fires autofs, which cascades into opendirectoryd/automountd
// lookups via /usr/libexec/od_user_homes. At bulk-remote-sync scale
// this pegs both daemons at 100s of % CPU, so the git-root walk
// must be skipped for such paths.
func TestExtractProjectFromCwd_ForeignPath_SkipsStatWalk(t *testing.T) {
	origPrefixes := autofsPrefixes
	defer func() { autofsPrefixes = origPrefixes }()
	autofsPrefixes = []string{"/home/"}

	orig := osStat
	defer func() { osStat = orig }()
	var count atomic.Int64
	osStat = func(path string) (os.FileInfo, error) {
		count.Add(1)
		return orig(path)
	}

	cwd := "/home/wes/code/example-project"
	want := "example_project"
	got := ExtractProjectFromCwdWithBranch(cwd, "")
	if got != want {
		t.Errorf("ExtractProjectFromCwdWithBranch(%q) = %q, want %q",
			cwd, got, want)
	}
	if n := count.Load(); n != 0 {
		t.Errorf("osStat called %d times for autofs-managed cwd %q; "+
			"expected 0 (git-root walk should be skipped)",
			n, cwd)
	}
}

// TestExtractProjectFromCwd_NativePath_StillWalks confirms that
// paths outside any autofs-managed prefix still trigger the
// git-root walk.
func TestExtractProjectFromCwd_NativePath_StillWalks(t *testing.T) {
	origPrefixes := autofsPrefixes
	defer func() { autofsPrefixes = origPrefixes }()
	autofsPrefixes = []string{"/home/"}

	orig := osStat
	defer func() { osStat = orig }()
	var count atomic.Int64
	osStat = func(path string) (os.FileInfo, error) {
		count.Add(1)
		return orig(path)
	}

	cwd := "/Users/nobody-agentsview-test/code/example"
	_ = ExtractProjectFromCwdWithBranch(cwd, "")
	if count.Load() == 0 {
		t.Errorf("osStat never called for %q; "+
			"git-root walk should run for non-autofs paths", cwd)
	}
}

// TestExtractProjectFromCwd_HomePathWithoutAutofs_StillWalks covers
// the edge case flagged in review: a user with a real filesystem
// mounted at /home (no autofs entry) should still get git-root
// resolution, not a basename-only fallback.
func TestExtractProjectFromCwd_HomePathWithoutAutofs_StillWalks(t *testing.T) {
	origPrefixes := autofsPrefixes
	defer func() { autofsPrefixes = origPrefixes }()
	autofsPrefixes = nil

	orig := osStat
	defer func() { osStat = orig }()
	var count atomic.Int64
	osStat = func(path string) (os.FileInfo, error) {
		count.Add(1)
		return orig(path)
	}

	cwd := "/home/nobody-agentsview-test/code/example"
	_ = ExtractProjectFromCwdWithBranch(cwd, "")
	if count.Load() == 0 {
		t.Errorf("osStat never called for /home path with empty " +
			"autofs config; walk must proceed for a real mount")
	}
}

// TestDetectAutofsPrefixes verifies that /etc/auto_master is parsed
// into the prefix set. Only darwin is expected to populate this;
// other platforms return an empty list.
func TestDetectAutofsPrefixes(t *testing.T) {
	origPath := autoMasterPath
	defer func() { autoMasterPath = origPath }()

	tmp := t.TempDir()
	fixture := filepath.Join(tmp, "auto_master")
	content := "#\n" +
		"# Automounter master map\n" +
		"#\n" +
		"+auto_master\n" +
		"#/net           -hosts    -nobrowse\n" +
		"/home           auto_home -nobrowse,hidefromfinder\n" +
		"/Network/Servers -fstab\n" +
		"/-              -static\n"
	if err := os.WriteFile(fixture, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	autoMasterPath = fixture

	got := detectAutofsPrefixes()
	if runtime.GOOS != "darwin" {
		if got != nil {
			t.Errorf("detectAutofsPrefixes() = %v on %s, want nil",
				got, runtime.GOOS)
		}
		return
	}

	want := []string{"/home/", "/Network/Servers/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detectAutofsPrefixes() = %v, want %v", got, want)
	}
}

// TestDetectAutofsPrefixes_MissingFile confirms that a missing
// auto_master file (unusual but possible) yields an empty list
// rather than crashing.
func TestDetectAutofsPrefixes_MissingFile(t *testing.T) {
	origPath := autoMasterPath
	defer func() { autoMasterPath = origPath }()
	autoMasterPath = filepath.Join(t.TempDir(), "does-not-exist")

	if got := detectAutofsPrefixes(); got != nil {
		t.Errorf("detectAutofsPrefixes() with missing file = %v, want nil",
			got)
	}
}
