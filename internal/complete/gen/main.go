// Command gen writes internal/complete/emotes_gen.go from the upstream
// ngchat checkout, invoked by the //go:generate directive at the top of
// internal/complete/complete.go. It never talks to the network and
// never guesses a checkout path: NGCHAT_UPSTREAM must point at one, the
// same drift contract ADR 0002 already documents for the protocol.
package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// upstreamEnvVar names the checkout ngen reads from. Kept as a constant
// so the error message and the lookup can't drift from each other.
const upstreamEnvVar = "NGCHAT_UPSTREAM"

// outFile is the generated source's path, relative to the working
// directory go generate runs the directive in (the package directory,
// per go generate's own contract), so this stays correct regardless of
// where "go run ./gen" itself was invoked from.
const outFile = "emotes_gen.go"

// emojiOutFile is emoji_gen.go's path, alongside outFile for the same
// reason.
const emojiOutFile = "emoji_gen.go"

// emojiCatalogPath is the upstream file's path, relative to the
// checkout root, that holds the generated emoji catalog this tool
// reads from.
const emojiCatalogPath = "src/client/app/chat/autocomplete/strategies/emoji_catalog.generated.ts"

// minEmoteCodes is a plausibility floor on readEmotes's result: well
// under upstream's actual count (1181 as of the commit this was
// written against), so a legitimate future trim of the lists would
// have to be drastic to trip it, but a wrong NGCHAT_UPSTREAM pointed
// at a checkout whose file layout moved (empty or near-empty JSON
// arrays) is caught here instead of silently shrinking the composer's
// emote list.
const minEmoteCodes = 1000

// minEmojiEntries is readEmoji's own plausibility floor, well under
// upstream's actual count (3991 as of the commit this was written
// against) for the same reason minEmoteCodes exists: a wrong
// NGCHAT_UPSTREAM or a moved catalog path fails loudly here instead of
// silently shrinking the composer's emoji list.
const minEmojiEntries = 3000

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run wires the environment, the two upstream sources and the commit
// lookup together, then writes both generated files. All the actual
// shaping happens in readEmotes/emoteSource and readEmoji/emojiSource,
// which take plain inputs so main_test.go can exercise them without a
// checkout.
//
// Both sources are read, validated and rendered to bytes in memory
// before either os.WriteFile call: writing emotes_gen.go as soon as
// it was ready (as an earlier version of this function did) meant an
// emoji-side failure after that point left the two committed files
// naming the same upstream commit in their headers while actually
// reflecting two different upstream trees — the previous run's emoji
// catalog next to a freshly regenerated emote list. Front-loading both
// reads means the only remaining way to get a partial regeneration is
// a write failure between the two os.WriteFile calls (a full disk,
// say), which is outside this tool's control either way.
func run() error {
	if err := ensureRunFromPackageDir("."); err != nil {
		return err
	}
	dir, err := upstreamDir()
	if err != nil {
		return err
	}
	codes, err := readEmotes(
		filepath.Join(dir, "src", "lib", "emoticons.json"),
		filepath.Join(dir, "src", "lib", "emoticons-small.json"),
	)
	if err != nil {
		return err
	}
	if err := requireMinCodes(dir, codes); err != nil {
		return err
	}
	entries, err := readEmoji(filepath.Join(dir, emojiCatalogPath))
	if err != nil {
		return err
	}
	if err := requireMinEmojiEntries(dir, entries); err != nil {
		return err
	}

	commit, err := upstreamCommit(dir, gitOutput)
	if err != nil {
		return err
	}

	emoteSrc, err := emoteSource(commit, codes)
	if err != nil {
		return err
	}
	emojiSrc, err := emojiSource(commit, entries)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outFile, emoteSrc, 0o644); err != nil {
		return err
	}
	return os.WriteFile(emojiOutFile, emojiSrc, 0o644)
}

// requireMinCodes errors when codes is implausibly short for a real
// upstream checkout, naming dir so the message points at what to check
// first (NGCHAT_UPSTREAM), rather than shrinking the composer's emote
// list silently if upstream's file layout ever moves.
func requireMinCodes(dir string, codes []string) error {
	if len(codes) < minEmoteCodes {
		return fmt.Errorf(
			"only %d shortcodes in %s; upstream layout probably moved",
			len(codes), dir)
	}
	return nil
}

// requireMinEmojiEntries is readEmoji's counterpart to requireMinCodes,
// against its own floor: the two catalogs have unrelated sizes, so one
// plausibility check must not stand in for the other.
func requireMinEmojiEntries(dir string, entries []emojiEntry) error {
	if len(entries) < minEmojiEntries {
		return fmt.Errorf(
			"only %d emoji entries in %s; upstream layout probably moved",
			len(entries), dir)
	}
	return nil
}

// ensureRunFromPackageDir refuses to write the generated file unless
// complete.go is present in dir: run from anywhere else (the repo
// root, in particular) this would otherwise drop emotes_gen.go, with
// its "package complete" clause, next to an unrelated go.mod instead
// of inside internal/complete.
func ensureRunFromPackageDir(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "complete.go")); err != nil {
		return fmt.Errorf(
			"run this via \"go generate ./internal/complete\": no complete.go in the working directory")
	}
	return nil
}

// upstreamDir reads NGCHAT_UPSTREAM, erroring rather than defaulting to
// a path when it is unset or empty: a silent default would let a stale
// or wrong checkout generate a list nobody asked for.
func upstreamDir() (string, error) {
	dir := os.Getenv(upstreamEnvVar)
	if dir == "" {
		return "", fmt.Errorf(
			"%s is unset; point it at a newgrounds-inc/ngchat checkout",
			upstreamEnvVar)
	}
	return dir, nil
}

// gitRunner runs "git -C dir <args...>" and returns its raw stdout;
// gitOutput is the production implementation. upstreamCommit takes one
// as a parameter rather than calling gitOutput directly so its
// toplevel guard — the whole point of this function — can be tested
// with a stub instead of a real git repository.
type gitRunner func(dir string, args ...string) ([]byte, error)

// upstreamCommit reports the short commit hash of the checkout at dir,
// recorded in the generated file's header so a reader can tell how
// stale the embedded list is without cross-referencing anything else.
// It first confirms dir is itself a repository root: "git -C dir
// rev-parse ..." walks up to the nearest enclosing .git regardless of
// dir, so a NGCHAT_UPSTREAM that names a plain data copy nested inside
// another repository (this one, say) would otherwise silently
// attribute the list to that unrelated repo's HEAD.
func upstreamCommit(dir string, git gitRunner) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	toplevel := strings.TrimSpace(string(out))
	same, err := sameDir(dir, toplevel)
	if err != nil {
		return "", err
	}
	if !same {
		return "", fmt.Errorf(
			"NGCHAT_UPSTREAM must be the root of the ngchat checkout, got %s", dir)
	}
	out, err = git(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sameDir reports whether a and b name the same directory, comparing
// file identity (os.SameFile, which Stat already resolves symlinks
// for) rather than the path strings: a relative NGCHAT_UPSTREAM would
// never string-equal git's absolute --show-toplevel answer even when
// it names the identical directory, and on a case-preserving but
// case-insensitive filesystem (macOS's default) two differently-cased
// spellings of the same path would wrongly compare unequal too. It
// takes two plain paths rather than shelling out itself so tests can
// exercise the comparison without a git repository.
func sameDir(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", b, err)
	}
	return os.SameFile(fa, fb), nil
}

// gitOutput is the production gitRunner: it runs "git -C dir
// <args...>" and returns its raw stdout (upstreamCommit trims it).
// exec's Output leaves *exec.ExitError.Stderr populated whenever
// Cmd.Stderr was nil (true here), so a failure's message carries
// git's own explanation (e.g. "not a git repository") rather than a
// bare exit status.
func gitOutput(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				return nil, fmt.Errorf("git %s in %s: %w: %s",
					strings.Join(args, " "), dir, err, stderr)
			}
		}
		return nil, fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), dir, err)
	}
	return out, nil
}

// readEmotes reads each path as a JSON array of shortcodes (upstream's
// emoticons.json and emoticons-small.json shape), unions them, drops
// duplicates (one shortcode repeats within emoticons.json itself as of
// the commit this was written against), appends "ngrandom" — upstream's
// own legacy addition, kept out of the JSON but added by hand at load
// time — unless a source list already carries it, and sorts
// case-insensitively with ties broken by the exact string so the order
// is stable regardless of which list contributed a name. Each entry is
// trimmed of surrounding whitespace before any of that, and an entry
// that trims to empty is dropped rather than offered as a shortcode
// nothing can render.
func readEmotes(paths ...string) ([]string, error) {
	seen := make(map[string]bool)
	var codes []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var list []string
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		for _, code := range list {
			code = strings.TrimSpace(code)
			if code == "" || seen[code] {
				continue
			}
			seen[code] = true
			codes = append(codes, code)
		}
	}
	if !seen["ngrandom"] {
		codes = append(codes, "ngrandom")
	}
	sort.Slice(codes, func(i, j int) bool {
		li, lj := strings.ToLower(codes[i]), strings.ToLower(codes[j])
		if li != lj {
			return li < lj
		}
		return codes[i] < codes[j]
	})
	return codes, nil
}

// emoteSource renders the generated file's full contents (header,
// package clause, the emoteCodes slice) and formats it with go/format,
// so the committed file is gofmt-clean without shelling out to gofmt.
// The header is emitted as a single line matching go.dev/s/generated
// code's `^// Code generated .* DO NOT EDIT\.$` pattern: split across
// two comment lines, tools that gate behavior on ast.IsGenerated (and
// on that convention generally) would silently stop treating the file
// as generated.
func emoteSource(commit string, codes []string) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b,
		"// Code generated by internal/complete/gen from newgrounds-inc/ngchat %s; DO NOT EDIT.\n\n",
		commit)
	b.WriteString("package complete\n\n")
	b.WriteString("// emoteCodes is every NG emote shortcode the composer can complete:\n")
	b.WriteString("// upstream's two emoticon lists plus \"ngrandom\", sorted.\n")
	b.WriteString("var emoteCodes = []string{\n")
	for _, code := range codes {
		fmt.Fprintf(&b, "\t%q,\n", code)
	}
	b.WriteString("}\n")
	return format.Source([]byte(b.String()))
}

// emojiEntry is one upstream catalog row after its glyph has been
// decoded from hex codepoints, ready to render into emoji_gen.go.
type emojiEntry struct {
	Name  string
	Glyph string
}

// emojiLinePattern matches one `[':shortname:', 'codepoints']` array
// row in upstream's emoji_catalog.generated.ts, anchored to the exact
// two-space indentation every real row carries (`(?m)^  \[`). The
// anchor is load-bearing, not decorative: the file's own leading
// doc comment contains one line of prose showing that same
// `[':interrobang:', '2049-fe0f']` shape as a worked example, and
// without the indentation anchor this pattern would parse that prose
// as a second, duplicate ":interrobang:" entry. Group 1 is the
// shortname without its delimiting colons; group 2 is the raw
// "-"-joined codepoints string, validated as hex by decodeGlyph rather
// than by this pattern, so a malformed part (upstream's own generator
// slipping, or this tool's assumptions drifting from its format)
// surfaces as readEmoji's error instead of a silent non-match.
var emojiLinePattern = regexp.MustCompile(`(?m)^  \[':([^':]+):', '([^']+)'\],$`)

// emojiRowStartPattern is a deliberately loose superset of
// emojiLinePattern: any line beginning (after leading whitespace) with
// "[" then a quote, whether or not the rest of that row's shape (the
// closing quote, comma and bracket) lands on the same line. Go's \s
// class includes "\n", so this also matches a row upstream's own
// prettier has wrapped across several lines (an open bracket alone on
// one line, the shortname and codepoints indented below it) — those
// wrapped rows are exactly what emojiLinePattern, being single-line,
// cannot see. readEmoji compares the two counts so a reformat that
// silently drops rows (rather than merely emptying the file, which
// requireMinEmojiEntries already catches) fails loudly instead of
// quietly shrinking the catalog by however many rows got wrapped.
var emojiRowStartPattern = regexp.MustCompile(`(?m)^\s*\[\s*'`)

// readEmoji parses path (upstream's emoji_catalog.generated.ts) for
// every `[':shortname:', 'codepoints']` row, decodes each entry's
// codepoints into its glyph at generate time so the row renderer never
// parses hex, and sorts the result by shortname. A codepoint that
// fails to parse is an error, not a skip: upstream's own generator
// guarantees well-formed hex, so a failure here means this tool's
// pattern or decoding has drifted from upstream's format, not that one
// bad row should quietly vanish from the catalog. The same is true of
// a row-shaped line emojiLinePattern fails to fully match: see
// emojiRowStartPattern's doc comment for why that signals a format
// change rather than a row this tool never had to understand.
func readEmoji(path string) ([]emojiEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	text := string(data)
	matches := emojiLinePattern.FindAllStringSubmatch(text, -1)
	rowStarts := len(emojiRowStartPattern.FindAllStringIndex(text, -1))
	if rowStarts > len(matches) {
		return nil, fmt.Errorf(
			"parsed %d of %d catalog rows in %s; upstream format changed",
			len(matches), rowStarts, path)
	}
	entries := make([]emojiEntry, 0, len(matches))
	for _, m := range matches {
		name, hex := m[1], m[2]
		glyph, err := decodeGlyph(hex)
		if err != nil {
			return nil, fmt.Errorf("emoji %q: %w", name, err)
		}
		entries = append(entries, emojiEntry{Name: name, Glyph: glyph})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// decodeGlyph joins hex's "-"-separated codepoints (each a lowercase
// hex string, e.g. "1f468-200d-2764-fe0f-200d-1f468") into the glyph
// they spell, in order, so a multi-codepoint sequence (a variation
// selector, a ZWJ family or couple) round-trips as the same rune
// sequence a browser would render from the same codepoints.
//
// strconv.ParseUint alone only rejects lexically malformed input: a
// value above unicode.MaxRune ("1f4afff") or a lone UTF-16 surrogate
// ("d83d", never a valid standalone Unicode scalar value) both parse
// cleanly as a uint32 and would otherwise decode to U+FFFD (the
// replacement character) with WriteRune, silently swapping the
// intended glyph for a mangled one instead of failing loudly the way
// every other malformed-input case here does.
func decodeGlyph(hex string) (string, error) {
	var b strings.Builder
	for _, part := range strings.Split(hex, "-") {
		v, err := strconv.ParseUint(part, 16, 32)
		if err != nil {
			return "", fmt.Errorf("parse codepoint %q: %w", part, err)
		}
		if v > unicode.MaxRune || (v >= 0xd800 && v <= 0xdfff) {
			return "", fmt.Errorf("codepoint %q is not a valid Unicode scalar value", part)
		}
		b.WriteRune(rune(v))
	}
	return b.String(), nil
}

// emojiSource renders emoji_gen.go's full contents, the same shape
// emoteSource produces for emotes_gen.go: a single-line generated-code
// header naming the upstream commit, the package clause, and the
// emojiCatalog slice, formatted with go/format so the committed file
// is gofmt-clean. Glyph is emitted via strconv.QuoteToASCII rather
// than a raw string literal, so the glyph column stays plain ASCII: a
// raw ZWJ sequence or variation selector sitting in the source would
// be invisible or misleading in a diff or an editor that renders it as
// a combined glyph instead of the individual runes it actually is.
// Name is emitted with %q instead, which is not similarly restricted
// to ASCII: it can carry a raw non-ASCII rune (upstream's one
// non-ASCII shortname, "piñata") since a plain accented letter is
// neither bidi- nor ZWJ-hazardous the way a glyph's own codepoints
// can be.
func emojiSource(commit string, entries []emojiEntry) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b,
		"// Code generated by internal/complete/gen from newgrounds-inc/ngchat %s; DO NOT EDIT.\n\n",
		commit)
	b.WriteString("package complete\n\n")
	b.WriteString("// emojiCatalog is every emoji shortname the composer can complete,\n")
	b.WriteString("// with its glyph decoded from upstream's codepoints at generate time\n")
	b.WriteString("// so the row renderer never parses hex.\n")
	b.WriteString("var emojiCatalog = []emoji{\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "\t{%q, %s},\n", e.Name, strconv.QuoteToASCII(e.Glyph))
	}
	b.WriteString("}\n")
	return format.Source([]byte(b.String()))
}
