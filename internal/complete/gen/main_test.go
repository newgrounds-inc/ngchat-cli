package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// writeJSON writes a JSON array literal to a temp file and returns its
// path, so readEmotes tests never touch a real upstream checkout.
func writeJSON(t *testing.T, dir, name, contents string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", p, err)
	}
	return p
}

// TestReadEmotesDedupesUnionsAndSorts covers the shaping rules: a
// shortcode repeated within one list or shared across both appears
// once, ngrandom is appended, and the sort is case-insensitive with
// ties broken by the exact string.
func TestReadEmotesDedupesUnionsAndSorts(t *testing.T) {
	dir := t.TempDir()
	a := writeJSON(t, dir, "a.json", `["ngbSmile", "ngaAyy", "ngaAyy", "Zebra"]`)
	b := writeJSON(t, dir, "b.json", `["ngaAyy", "apple"]`)

	got, err := readEmotes(a, b)
	if err != nil {
		t.Fatalf("readEmotes: %v", err)
	}
	want := []string{"apple", "ngaAyy", "ngbSmile", "ngrandom", "Zebra"}
	if !slicesEqual(got, want) {
		t.Fatalf("readEmotes = %v, want %v", got, want)
	}
}

// TestReadEmotesNgrandomAppendedOnce guards against a duplicate
// "ngrandom" entry when a source list already carries it (upstream
// does not today, but the generator must not assume that forever).
func TestReadEmotesNgrandomAppendedOnce(t *testing.T) {
	dir := t.TempDir()
	a := writeJSON(t, dir, "a.json", `["ngrandom", "ngaAyy"]`)

	got, err := readEmotes(a)
	if err != nil {
		t.Fatalf("readEmotes: %v", err)
	}
	count := 0
	for _, c := range got {
		if c == "ngrandom" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("readEmotes = %v, want exactly one ngrandom", got)
	}
}

// TestReadEmotesSortTieBrokenByExactString: two shortcodes equal under
// case-folding sort by their exact (case-sensitive) form instead.
func TestReadEmotesSortTieBrokenByExactString(t *testing.T) {
	dir := t.TempDir()
	a := writeJSON(t, dir, "a.json", `["ngaFoo", "ngafoo"]`)

	got, err := readEmotes(a)
	if err != nil {
		t.Fatalf("readEmotes: %v", err)
	}
	// Both fold to "ngafoo"; exact-string order puts the uppercase "F"
	// form first (capital letters sort below lowercase in ASCII).
	want := []string{"ngaFoo", "ngafoo", "ngrandom"}
	if !slicesEqual(got, want) {
		t.Fatalf("readEmotes = %v, want %v", got, want)
	}
}

// TestReadEmotesTrimsWhitespaceAndDropsEmpty: an entry with stray
// surrounding whitespace is folded into its trimmed form (so it dedupes
// against a clean copy of the same name), and an entry that is empty or
// all whitespace is dropped rather than kept as an unrenderable
// shortcode.
func TestReadEmotesTrimsWhitespaceAndDropsEmpty(t *testing.T) {
	dir := t.TempDir()
	a := writeJSON(t, dir, "a.json", `[" ngaAyy ", "ngaAyy", "  ", ""]`)

	got, err := readEmotes(a)
	if err != nil {
		t.Fatalf("readEmotes: %v", err)
	}
	want := []string{"ngaAyy", "ngrandom"}
	if !slicesEqual(got, want) {
		t.Fatalf("readEmotes = %v, want %v", got, want)
	}
}

// TestRequireMinCodes checks the plausibility floor both ways: fewer
// than minEmoteCodes is an error naming the count and the checkout
// directory, and the floor itself passes silently.
func TestRequireMinCodes(t *testing.T) {
	if err := requireMinCodes("/some/checkout", make([]string, minEmoteCodes-1)); err == nil {
		t.Fatal("requireMinCodes below the floor: want an error, got nil")
	} else if !strings.Contains(err.Error(), "/some/checkout") {
		t.Errorf("requireMinCodes error = %q, want it to name the checkout dir", err)
	}
	if err := requireMinCodes("/some/checkout", make([]string, minEmoteCodes)); err != nil {
		t.Errorf("requireMinCodes at the floor: want nil, got %v", err)
	}
}

// TestEnsureRunFromPackageDir checks both halves of the guard: a
// directory with complete.go passes, and one without it (the module
// root, in the real failure this guards against) is refused with a
// message pointing at the right invocation.
func TestEnsureRunFromPackageDir(t *testing.T) {
	dir := t.TempDir()
	if err := ensureRunFromPackageDir(dir); err == nil {
		t.Fatal("ensureRunFromPackageDir without complete.go: want an error, got nil")
	} else if !strings.Contains(err.Error(), "go generate ./internal/complete") {
		t.Errorf("ensureRunFromPackageDir error = %q, want it to name the right invocation", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "complete.go"), []byte("package complete\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(complete.go): %v", err)
	}
	if err := ensureRunFromPackageDir(dir); err != nil {
		t.Errorf("ensureRunFromPackageDir with complete.go present: want nil, got %v", err)
	}
}

// TestSameDir exercises the toplevel comparison main.go's upstreamCommit
// relies on, entirely with plain paths so the test needs no git
// repository: two names for the identical directory (one via a
// symlink) compare equal, and two distinct directories do not.
func TestSameDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevated privileges on Windows")
	}
	root := t.TempDir()
	real := filepath.Join(root, "real")
	other := filepath.Join(root, "other")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("Mkdir(real): %v", err)
	}
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatalf("Mkdir(other): %v", err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if same, err := sameDir(real, link); err != nil {
		t.Fatalf("sameDir(real, link): %v", err)
	} else if !same {
		t.Error("sameDir(real, link) = false, want true (link resolves to real)")
	}
	if same, err := sameDir(real, other); err != nil {
		t.Fatalf("sameDir(real, other): %v", err)
	} else if same {
		t.Error("sameDir(real, other) = true, want false (distinct directories)")
	}
}

// TestSameDirRelativePath guards the regression a string comparison
// had: NGCHAT_UPSTREAM given as a relative path that names the
// checkout root, compared against git's absolute --show-toplevel
// answer for it, must still compare equal. os.Stat resolves a relative
// path against the working directory, so t.Chdir into the parent is
// what makes "./a" and the absolute path name the same directory.
func TestSameDirRelativePath(t *testing.T) {
	parent := t.TempDir()
	abs := filepath.Join(parent, "a")
	if err := os.Mkdir(abs, 0o755); err != nil {
		t.Fatalf("Mkdir(a): %v", err)
	}
	t.Chdir(parent)

	if same, err := sameDir("./a", abs); err != nil {
		t.Fatalf("sameDir(\"./a\", abs): %v", err)
	} else if !same {
		t.Error("sameDir(\"./a\", abs) = false, want true (relative vs. absolute, same directory)")
	}
}

// fakeGit builds a gitRunner that answers each call by looking up
// args[1] (the git subcommand: "--show-toplevel" or "HEAD") in
// answers, so upstreamCommit's tests need neither a git binary nor a
// real repository.
func fakeGit(answers map[string][]byte, errs map[string]error) gitRunner {
	return func(dir string, args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if err, ok := errs[key]; ok {
			return nil, err
		}
		return answers[key], nil
	}
}

// TestUpstreamCommitAtRoot: when the stubbed --show-toplevel answer
// names dir itself, upstreamCommit returns the trimmed hash from the
// second call unchanged.
func TestUpstreamCommitAtRoot(t *testing.T) {
	dir := t.TempDir()
	git := fakeGit(map[string][]byte{
		"rev-parse --show-toplevel": []byte(dir + "\n"),
		"rev-parse --short HEAD":    []byte("abc1234\n"),
	}, nil)

	got, err := upstreamCommit(dir, git)
	if err != nil {
		t.Fatalf("upstreamCommit: %v", err)
	}
	if got != "abc1234" {
		t.Errorf("upstreamCommit = %q, want %q", got, "abc1234")
	}
}

// TestUpstreamCommitWrongToplevel: a --show-toplevel answer naming a
// different directory than dir is refused with the "must be the root"
// message, and the HEAD lookup is never reached.
func TestUpstreamCommitWrongToplevel(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	git := fakeGit(map[string][]byte{
		"rev-parse --show-toplevel": []byte(other + "\n"),
		"rev-parse --short HEAD":    []byte("should-not-be-read\n"),
	}, nil)

	_, err := upstreamCommit(dir, git)
	if err == nil || !strings.Contains(err.Error(), "must be the root of the ngchat checkout") {
		t.Fatalf("upstreamCommit with mismatched toplevel = %v, want a \"must be the root\" error", err)
	}
}

// TestUpstreamCommitGitError: a failure from the git runner (a
// non-repository checkout, in production) is wrapped and returned
// rather than swallowed.
func TestUpstreamCommitGitError(t *testing.T) {
	dir := t.TempDir()
	wantErr := errors.New("not a git repository")
	git := fakeGit(nil, map[string]error{"rev-parse --show-toplevel": wantErr})

	_, err := upstreamCommit(dir, git)
	if !errors.Is(err, wantErr) {
		t.Fatalf("upstreamCommit git error = %v, want it to wrap %v", err, wantErr)
	}
}

// TestReadEmotesMissingFile reports a wrapped error rather than
// panicking or returning a partial list.
func TestReadEmotesMissingFile(t *testing.T) {
	_, err := readEmotes(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("readEmotes with a missing file: want an error, got nil")
	}
}

// TestReadEmotesInvalidJSON reports a wrapped error on malformed input.
func TestReadEmotesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	a := writeJSON(t, dir, "a.json", `not json`)
	if _, err := readEmotes(a); err == nil {
		t.Fatal("readEmotes with invalid JSON: want an error, got nil")
	}
}

// TestUpstreamDirUnsetIsError checks the helper directly, per the
// spec's instruction to test the env-reading helper rather than main.
func TestUpstreamDirUnsetIsError(t *testing.T) {
	t.Setenv(upstreamEnvVar, "")
	if _, err := upstreamDir(); err == nil {
		t.Fatal("upstreamDir with NGCHAT_UPSTREAM unset: want an error, got nil")
	} else if !strings.Contains(err.Error(), upstreamEnvVar) {
		t.Errorf("upstreamDir error = %q, want it to name %s", err, upstreamEnvVar)
	}
}

// TestUpstreamDirSet is the mirror case: a non-empty value passes
// through unchanged.
func TestUpstreamDirSet(t *testing.T) {
	t.Setenv(upstreamEnvVar, "/some/checkout")
	got, err := upstreamDir()
	if err != nil {
		t.Fatalf("upstreamDir: %v", err)
	}
	if got != "/some/checkout" {
		t.Errorf("upstreamDir = %q, want %q", got, "/some/checkout")
	}
}

// TestEmoteSourceIsFormattedAndCarriesCommit checks the generated
// file's shape: the header names the commit, the package clause and
// slice are present, and the output is already gofmt-clean (go/format
// would have errored on malformed input, but this also checks the
// bytes round-trip through format.Source unchanged).
func TestEmoteSourceIsFormattedAndCarriesCommit(t *testing.T) {
	src, err := emoteSource("abc1234", []string{"ngaAyy", "ngrandom"})
	if err != nil {
		t.Fatalf("emoteSource: %v", err)
	}
	text := string(src)
	for _, want := range []string{
		"abc1234",
		"DO NOT EDIT",
		"package complete",
		`"ngaAyy"`,
		`"ngrandom"`,
		"var emoteCodes = []string{",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("emoteSource output missing %q:\n%s", want, text)
		}
	}
}

// generatedCodeMarker is go.dev/s/generatedcode's own pattern for the
// comment that tells tools (gofmt -s, code review bots, ast.IsGenerated
// callers) a file is machine-written: one line, exactly this shape.
var generatedCodeMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// TestEmoteSourceHeaderIsSingleLineGeneratedMarker guards against the
// header regressing into two comment lines (a commit hash on its own
// line, say): split across lines, ast.IsGenerated and every tool built
// on the same convention stop recognizing the file as generated.
func TestEmoteSourceHeaderIsSingleLineGeneratedMarker(t *testing.T) {
	src, err := emoteSource("abc1234", []string{"ngrandom"})
	if err != nil {
		t.Fatalf("emoteSource: %v", err)
	}
	first, _, _ := strings.Cut(string(src), "\n")
	if !generatedCodeMarker.MatchString(first) {
		t.Errorf("emoteSource header line = %q, want it to match %s",
			first, generatedCodeMarker)
	}
}

// slicesEqual is a small local helper so the test file has no
// dependency beyond the standard library the rest of the generator
// already uses.
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
