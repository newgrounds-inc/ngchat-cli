package complete

import (
	"regexp"
	"sort"
	"strings"
)

// Access is the server's role gate on a command, mirrored here only to
// hide dead ends from the list (see CommandTable's doc comment for why
// the server, not this package, is the actual enforcement point).
// Values are a rank in ascending order: Candidates keeps a command
// when cmd.Access <= Viewer().
type Access int

const (
	AccessAll   Access = iota // no role check
	AccessMod                 // admin or chat mod
	AccessAdmin               // admin only; chat mods are refused too
)

// Command is one row of the slash-command table.
type Command struct {
	// Name is the canonical command, with the leading "/".
	Name string
	// Aliases are accepted alternate names, without the leading "/".
	Aliases []string
	// Access is the role tier the server's handler enforces.
	Access Access
	// Description is the short, human-facing summary shown dimmed in
	// the completion list, taken verbatim from upstream.
	Description string
}

// CommandTable is the hand-ported copy of upstream's SLASH_COMMANDS
// (32 rows) plus the CLI-local /who, reconciled by hand against
// upstream commit b54377d7
// (src/client/app/chat/autocomplete/slash_commands.ts) under ADR
// 0002's drift contract, extended to this table by AGENTS.md's
// internal/complete paragraph. The table keeps upstream's own order
// (already alphabetical by name), with /who appended after it, so a
// diff against upstream's SLASH_COMMANDS array is a straight read;
// Candidates re-sorts its filtered result, so this declaration order
// is never itself relied on for display. Access is mirrored here only
// to hide dead ends from the list — the server is the only
// enforcement point, exactly as upstream's own getSlashCommands
// documents.
var CommandTable = []Command{
	{Name: "/ah", Aliases: []string{"airhorn"}, Access: AccessMod,
		Description: "makes a user's client play an airhorn"},
	{Name: "/alts", Aliases: nil, Access: AccessMod,
		Description: "checks for potential alt accounts"},
	{Name: "/away", Aliases: []string{"a"}, Access: AccessAll,
		Description: "sets yourself away"},
	{Name: "/back", Aliases: []string{"ba"}, Access: AccessAll,
		Description: "sets yourself back"},
	{Name: "/ban", Aliases: []string{"b"}, Access: AccessMod,
		Description: "bans a user"},
	{Name: "/block", Aliases: nil, Access: AccessAll,
		Description: "manage blocked users"},
	{Name: "/block/add", Aliases: nil, Access: AccessAll,
		Description: "block a user"},
	{Name: "/block/list", Aliases: nil, Access: AccessAll,
		Description: "list blocked users"},
	{Name: "/block/remove", Aliases: nil, Access: AccessAll,
		Description: "remove a blocked user"},
	{Name: "/disconnect", Aliases: []string{"dc"}, Access: AccessAdmin,
		Description: "silently disconnects a user"},
	{Name: "/dm", Aliases: nil, Access: AccessAll,
		Description: "sends a direct message to a user"},
	{Name: "/gimme", Aliases: nil, Access: AccessAll,
		Description: "sends ༆ つ ◕_◕ ༽つ with an optional message"},
	{Name: "/help", Aliases: []string{"h"}, Access: AccessAll,
		Description: "shows chat usage"},
	{Name: "/help/emoji", Aliases: nil, Access: AccessAll,
		Description: "shows emoji/emoticon usage"},
	{Name: "/ignore", Aliases: nil, Access: AccessAll,
		Description: "manage ignored users"},
	{Name: "/ignore/add", Aliases: nil, Access: AccessAll,
		Description: "ignore a user"},
	{Name: "/ignore/list", Aliases: nil, Access: AccessAll,
		Description: "list ignored users"},
	{Name: "/ignore/remove", Aliases: nil, Access: AccessAll,
		Description: "remove an ignored user"},
	{Name: "/kick", Aliases: []string{"k"}, Access: AccessMod,
		Description: "kicks a user"},
	{Name: "/lenny", Aliases: nil, Access: AccessAll,
		Description: "sends ( ͡° ͜ʖ ͡°) with an optional message"},
	{Name: "/me", Aliases: nil, Access: AccessAll,
		Description: "sends a me message"},
	{Name: "/play", Aliases: nil, Access: AccessAdmin,
		Description: "plays a sound effect for the channel"},
	{Name: "/random", Aliases: nil, Access: AccessAll,
		Description: "sends a random dank meme plus an optional message"},
	{Name: "/roll", Aliases: []string{"r"}, Access: AccessAll,
		Description: "rolls the dice (NdS)"},
	{Name: "/sfx", Aliases: nil, Access: AccessAll,
		Description: "lists sound effects or triggers one by id or alias"},
	{Name: "/shrug", Aliases: nil, Access: AccessAll,
		Description: "sends ¯\\_(ツ)_/¯ with an optional message"},
	{Name: "/slap", Aliases: []string{"s"}, Access: AccessAll,
		Description: "slaps users"},
	{Name: "/spoiler", Aliases: nil, Access: AccessAll,
		Description: "covers your message with a spoiler / NSFW warning"},
	{Name: "/table", Aliases: nil, Access: AccessAll,
		Description: "sends (╯°□°）╯︵ ┻━┻ with an optional message"},
	{Name: "/table/r", Aliases: nil, Access: AccessAll,
		Description: "sends (┬─┬ノ( º _ ºノ) with an optional message"},
	{Name: "/unban", Aliases: []string{"ub"}, Access: AccessMod,
		Description: "unbans a user"},
	{Name: "/yippee", Aliases: nil, Access: AccessAll,
		Description: "sends ᕕ( ᐛ )ᕗ with an optional message"},
	// CLI-local: /who has no upstream handler (the web client renders
	// its own roster panel instead), so it is handled entirely in
	// internal/ui rather than sent to the server; see model.go's
	// isWho/whoText.
	{Name: "/who", Aliases: nil, Access: AccessAll,
		Description: "lists who is in the channel (handled locally)"},
}

// commandTrigger fires only when "/" is the first character of the
// line and everything after it (up to the cursor) is a bare candidate
// prefix: no argument text, so "/ban bob" mid-command never reopens
// the list once a name has been typed after it. Ported from upstream's
// slash_strategy.tsx match regex, `/i` folded into the Go flag since
// Go's character class below is already lowercase-only.
var commandTrigger = regexp.MustCompile(`(?i)^/([a-z0-9/*]*)$`)

// Commands completes slash commands for a viewer of a given rank.
type Commands struct {
	// Viewer reports the caller's rank, read fresh on every query so a
	// revalidated token's flags are reflected without a hook back into
	// this package. Required: Candidates calls it unguarded, so a
	// zero-value Commands panics on first use rather than silently
	// completing nothing (the same contract Mentions's Users and Self
	// fields carry).
	Viewer func() Access
}

// Name identifies the command source for Engine's sticky dismiss.
func (Commands) Name() string { return "command" }

// Match reports the span of an open command line, per commandTrigger.
// The whole line up to the cursor must be the trigger (start is always
// 0), which is what keeps "/x y" mid-line from opening a list once a
// space has been typed. Only line[:cursor] is consulted, per Source's
// contract: with the cursor inside a half-typed name ("/ki|ck", cursor
// right after "ki"), the span is [0, cursor) ("/ki") and accepting
// splices the canonical name in ahead of the untouched "ck" tail, the
// same as every other source.
func (c Commands) Match(line string, cursor int) (start int, term string, ok bool) {
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(line) {
		cursor = len(line)
	}
	head := line[:cursor]
	loc := commandTrigger.FindStringSubmatchIndex(head)
	if loc == nil {
		return 0, "", false
	}
	return 0, head[loc[2]:loc[3]], true
}

// Candidates filters CommandTable to the viewer's rank, then applies
// upstream's own matching rule (never the fuzzy Rank every other
// source uses, per ADR 0006): term is split on "*", each piece is
// regexp-escaped and rejoined with ".*", and the resulting pattern is
// matched, case-insensitively, as a prefix of the command's name or of
// "/" plus any alias. A term the trigger's character class produced
// cannot make regexp.Compile fail, but a nil result rather than a
// panic is returned if it ever does. The result is sorted by Name
// (plain string order, which is what upstream's localeCompare gives
// for these ASCII names) and is not capped here; Engine.Complete caps
// at MaxCandidates.
func (c Commands) Candidates(term string) []Candidate {
	pieces := strings.Split(term, "*")
	for i, p := range pieces {
		pieces[i] = regexp.QuoteMeta(p)
	}
	pattern, err := regexp.Compile(`(?i)^/` + strings.Join(pieces, ".*"))
	if err != nil {
		return nil
	}
	viewer := c.Viewer()
	var matched []Command
	for _, cmd := range CommandTable {
		if cmd.Access > viewer {
			continue
		}
		if pattern.MatchString(cmd.Name) {
			matched = append(matched, cmd)
			continue
		}
		for _, alias := range cmd.Aliases {
			if pattern.MatchString("/" + alias) {
				matched = append(matched, cmd)
				break
			}
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	out := make([]Candidate, len(matched))
	for i, cmd := range matched {
		label := cmd.Name
		if len(cmd.Aliases) > 0 {
			label += " (" + strings.Join(cmd.Aliases, ", ") + ")"
		}
		out[i] = Candidate{Insert: cmd.Name + " ", Label: label, Detail: cmd.Description}
	}
	return out
}

// AccessFor maps the wire's privilege flags onto a rank: admin wins
// over mod, and neither flag set is the everyone tier.
func AccessFor(isAdmin, isChatMod bool) Access {
	switch {
	case isAdmin:
		return AccessAdmin
	case isChatMod:
		return AccessMod
	default:
		return AccessAll
	}
}
