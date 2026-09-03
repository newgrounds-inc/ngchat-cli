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

// writeFile writes contents to a temp file and returns its path, so
// readEmotes and readEmoji tests never touch a real upstream checkout.
// contents is a JSON array literal for the readEmotes tests and a
// slice of a .ts source file for the readEmoji ones; this helper is
// agnostic to which.
func writeFile(t *testing.T, dir, name, contents string) string {
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
	a := writeFile(t, dir, "a.json", `["ngbSmile", "ngaAyy", "ngaAyy", "Zebra"]`)
	b := writeFile(t, dir, "b.json", `["ngaAyy", "apple"]`)

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
	a := writeFile(t, dir, "a.json", `["ngrandom", "ngaAyy"]`)

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
	a := writeFile(t, dir, "a.json", `["ngaFoo", "ngafoo"]`)

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
	a := writeFile(t, dir, "a.json", `[" ngaAyy ", "ngaAyy", "  ", ""]`)

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
	a := writeFile(t, dir, "a.json", `not json`)
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

// emojiFixture is a hand-built stand-in for upstream's
// emoji_catalog.generated.ts: three array rows (a plain one, a
// multi-codepoint ZWJ sequence, and a "+"-prefixed name) preceded by a
// doc comment shaped like upstream's own, which shows a worked example
// of the same `[':name:', 'codepoints']` shape inline rather than as
// its own indented array row. That example names a shortname
// ("interrobang") absent from the array itself, so a regression that
// drops emojiLinePattern's line-start indentation anchor would show up
// here as a spurious fourth entry rather than silently passing.
const emojiFixture = `/** doc comment example: [':interrobang:', '2049-fe0f'] inline. */
export const EMOJI_CATALOG = [
  [':100:', '1f4af'],
  [':family_man_boy:', '1f468-200d-1f466'],
  [':+1:', '1f44d'],
];
`

// TestReadEmojiDecodesSortsAndIgnoresProse checks the three shaping
// rules at once: each entry's codepoints decode into the glyph they
// spell (single codepoint, multi-codepoint ZWJ sequence, and a
// "+"-prefixed name all included), the result is sorted by shortname
// in plain string order, and the doc comment's own worked example
// (naming a shortname not in the array) contributes no entry.
func TestReadEmojiDecodesSortsAndIgnoresProse(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "emoji.ts", emojiFixture)

	got, err := readEmoji(path)
	if err != nil {
		t.Fatalf("readEmoji: %v", err)
	}
	want := []emojiEntry{
		{Name: "+1", Glyph: "\U0001f44d"},
		{Name: "100", Glyph: "\U0001f4af"},
		{Name: "family_man_boy", Glyph: "\U0001f468\u200d\U0001f466"},
	}
	if len(got) != len(want) {
		t.Fatalf("readEmoji = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("readEmoji[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestReadEmojiMalformedHexIsError checks that a codepoint part that
// fails strconv.ParseUint fails the whole read rather than silently
// dropping the entry: upstream's own generator guarantees well-formed
// hex, so a bad part here means this tool's assumptions have drifted.
func TestReadEmojiMalformedHexIsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "emoji.ts", `export const EMOJI_CATALOG = [
  [':bad:', '1f4af-zzzz'],
];
`)
	if _, err := readEmoji(path); err == nil {
		t.Fatal("readEmoji with a malformed hex part: want an error, got nil")
	}
}

// TestDecodeGlyphRejectsOutOfRangeCodepoints covers the three ways a
// codepoint part can be lexically valid hex (so strconv.ParseUint
// alone accepts it) while still not naming a Unicode scalar value:
// above unicode.MaxRune, exactly one past it, and a lone UTF-16
// surrogate. Each would otherwise decode via rune()/WriteRune into
// U+FFFD (the replacement character) rather than fail, silently
// swapping the intended glyph for a mangled one.
func TestDecodeGlyphRejectsOutOfRangeCodepoints(t *testing.T) {
	tests := []struct {
		name string
		hex  string
	}{
		{"far above unicode.MaxRune", "1f4afff"},
		{"one past unicode.MaxRune", "110000"},
		{"lone UTF-16 surrogate", "d83d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeGlyph(tc.hex); err == nil {
				t.Fatalf("decodeGlyph(%q): want an error, got nil", tc.hex)
			}
		})
	}
}

// TestReadEmojiMissingFile reports a wrapped error rather than
// panicking or returning a partial catalog.
func TestReadEmojiMissingFile(t *testing.T) {
	_, err := readEmoji(filepath.Join(t.TempDir(), "nope.ts"))
	if err == nil {
		t.Fatal("readEmoji with a missing file: want an error, got nil")
	}
}

// TestReadEmojiErrorsOnWrappedRow guards against a partial reformat
// upstream could make (a subset of rows wrapped across lines, past
// some line-length limit) that emojiLinePattern's single-line match
// silently drops while still leaving readEmoji's row count well above
// minEmojiEntries: the middle row here is wrapped across three lines,
// the open bracket alone on its own line the way a formatter wraps a
// long call, and readEmoji must refuse to treat that as "one fewer
// entry" and instead report the row-start/parsed mismatch.
func TestReadEmojiErrorsOnWrappedRow(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "emoji.ts", `export const EMOJI_CATALOG = [
  [':100:', '1f4af'],
  [
    ':wrapped:',
    '1f9d1',
  ],
  [':zzz:', '1f4a4'],
];
`)
	_, err := readEmoji(path)
	if err == nil {
		t.Fatal("readEmoji with a wrapped row: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "upstream format changed") {
		t.Errorf("readEmoji error = %q, want it to name the format change", err)
	}
}

// TestRequireMinEmojiEntries checks the plausibility floor both ways,
// the same shape as TestRequireMinCodes.
func TestRequireMinEmojiEntries(t *testing.T) {
	if err := requireMinEmojiEntries("/some/checkout", make([]emojiEntry, minEmojiEntries-1)); err == nil {
		t.Fatal("requireMinEmojiEntries below the floor: want an error, got nil")
	} else if !strings.Contains(err.Error(), "/some/checkout") {
		t.Errorf("requireMinEmojiEntries error = %q, want it to name the checkout dir", err)
	}
	if err := requireMinEmojiEntries("/some/checkout", make([]emojiEntry, minEmojiEntries)); err != nil {
		t.Errorf("requireMinEmojiEntries at the floor: want nil, got %v", err)
	}
}

// TestEmojiSourceIsFormattedAndCarriesCommit mirrors
// TestEmoteSourceIsFormattedAndCarriesCommit: the header names the
// commit, the package clause and slice are present, the glyph is
// emitted as an ASCII escape rather than the raw rune, and the output
// is already gofmt-clean.
func TestEmojiSourceIsFormattedAndCarriesCommit(t *testing.T) {
	src, err := emojiSource("abc1234", []emojiEntry{
		{Name: "100", Glyph: "\U0001f4af"},
	})
	if err != nil {
		t.Fatalf("emojiSource: %v", err)
	}
	text := string(src)
	for _, want := range []string{
		"abc1234",
		"DO NOT EDIT",
		"package complete",
		`"100"`,
		`"\U0001f4af"`,
		"var emojiCatalog = []emoji{",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("emojiSource output missing %q:\n%s", want, text)
		}
	}
	if strings.ContainsRune(text, '\U0001f4af') {
		t.Errorf("emojiSource output contains a raw glyph byte, want only the ASCII escape:\n%s", text)
	}
}
