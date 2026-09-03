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
	"sort"
	"strings"
)

// upstreamEnvVar names the checkout ngen reads from. Kept as a constant
// so the error message and the lookup can't drift from each other.
const upstreamEnvVar = "NGCHAT_UPSTREAM"

// outFile is the generated source's path, relative to the working
// directory go generate runs the directive in (the package directory,
// per go generate's own contract), so this stays correct regardless of
// where "go run ./gen" itself was invoked from.
const outFile = "emotes_gen.go"

// minEmoteCodes is a plausibility floor on readEmotes's result: well
// under upstream's actual count (1181 as of the commit this was
// written against), so a legitimate future trim of the lists would
// have to be drastic to trip it, but a wrong NGCHAT_UPSTREAM pointed
// at a checkout whose file layout moved (empty or near-empty JSON
// arrays) is caught here instead of silently shrinking the composer's
// emote list.
const minEmoteCodes = 1000

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run wires the environment, the two upstream JSON paths and the
// commit lookup together, then writes the generated file. All the
// actual shaping happens in readEmotes and emoteSource, which take
// plain inputs so main_test.go can exercise them without a checkout.
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
	commit, err := upstreamCommit(dir, gitOutput)
	if err != nil {
		return err
	}
	src, err := emoteSource(commit, codes)
	if err != nil {
		return err
	}
	return os.WriteFile(outFile, src, 0o644)
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
