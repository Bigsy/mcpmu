package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCanonicalConfigPathExistingSymlinkAndAbsentParents(t *testing.T) {
	realDir := t.TempDir()
	realConfig := filepath.Join(realDir, "config.json")
	if err := os.WriteFile(realConfig, []byte(`{"schemaVersion":1,"servers":{},"namespaces":{}}`), 0600); err != nil {
		t.Fatal(err)
	}

	linkRoot := t.TempDir()
	link := filepath.Join(linkRoot, "config-link")
	if err := os.Symlink(realConfig, link); err != nil {
		t.Fatal(err)
	}
	canonicalReal, err := CanonicalConfigPath(realConfig)
	if err != nil {
		t.Fatal(err)
	}
	canonicalLink, err := CanonicalConfigPath(link)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalLink != canonicalReal {
		t.Fatalf("symlink canonicalized to %q, want %q", canonicalLink, canonicalReal)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(workingDir, realConfig)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRelative, err := CanonicalConfigPath(relative)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalRelative != canonicalReal {
		t.Fatalf("relative path canonicalized to %q, want %q", canonicalRelative, canonicalReal)
	}

	absent := filepath.Join(linkRoot, "missing", "nested", "config.json")
	canonicalAbsent, err := CanonicalConfigPath(absent)
	if err != nil {
		t.Fatal(err)
	}
	resolvedLinkRoot, err := filepath.EvalSymlinks(linkRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantAbsent := filepath.Join(resolvedLinkRoot, "missing", "nested", "config.json")
	if canonicalAbsent != wantAbsent {
		t.Fatalf("absent path canonicalized to %q, want %q", canonicalAbsent, wantAbsent)
	}

	directoryLink := filepath.Join(linkRoot, "directory-link")
	if err := os.Symlink(realDir, directoryLink); err != nil {
		t.Fatal(err)
	}
	throughLinkedAncestor := filepath.Join(directoryLink, "future", "config.json")
	canonicalLinkedAncestor, err := CanonicalConfigPath(throughLinkedAncestor)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRealDir, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	wantLinkedAncestor := filepath.Join(resolvedRealDir, "future", "config.json")
	if canonicalLinkedAncestor != wantLinkedAncestor {
		t.Fatalf("linked ancestor canonicalized to %q, want %q", canonicalLinkedAncestor, wantLinkedAncestor)
	}
}

func TestCanonicalConfigPathExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := CanonicalConfigPath("~/missing/config.json")
	if err != nil {
		t.Fatal(err)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvedHome, "missing", "config.json")
	if got != want {
		t.Fatalf("CanonicalConfigPath() = %q, want %q", got, want)
	}
}

func TestRuntimePathsPrivateAndShort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets are unsupported on windows")
	}
	runtimeRoot := makeShortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	paths, err := RuntimePaths(filepath.Join(runtimeRoot, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.RuntimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0700 {
		t.Fatalf("runtime dir mode = %o, want 700", got)
	}
	if len(paths.Socket) > maxUnixSocketPath {
		t.Fatalf("socket path length = %d, want <= %d", len(paths.Socket), maxUnixSocketPath)
	}
	if !strings.HasSuffix(paths.Socket, ".sock") || !strings.HasSuffix(paths.PIDFile, ".pid") || !strings.HasSuffix(paths.LogFile, ".log") {
		t.Fatalf("unexpected runtime paths: %+v", paths)
	}
}

func makeShortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mu-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
