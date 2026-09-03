package complete

import (
	"sort"
	"strings"
)

// Rank orders items by how closely key(item) matches term,
// case-insensitively: a prefix match ranks above a substring match,
// which ranks above a subsequence match (every rune of term appears in
// key(item), in order, not necessarily adjacent). Items matching none
// of the three are dropped. Within a tier, items sort alphabetically
// (case-insensitive); a tie on the lowercased key (e.g. a roster
// carrying both "Bob" and "bob") breaks on the exact key instead of
// falling back to input order, so the result stays the same regardless
// of which order a caller's map range happened to visit them in. Items
// tied on the exact key too keep their input order (sort.SliceStable),
// which is what makes a source's own ordering (e.g. upstream's static
// command table) survive a genuine tie. An empty term matches
// everything at the prefix tier, so the result is every item,
// alphabetical.
//
// This is the CLI's in-house departure from upstream's fuzzysort
// (ADR 0006): deterministic, dependency-free, and close enough to what
// a person expects when they don't know the exact name.
func Rank[T any](term string, items []T, key func(T) string) []T {
	// lower and exact are carried alongside the item so the comparator
	// below never recomputes a key it has already read once per item.
	type scored struct {
		item  T
		tier  int
		lower string
		exact string
	}
	lowerTerm := strings.ToLower(term)
	// term is decoded to runes once here rather than inside
	// isSubsequence per item. The remaining per-keystroke cost is
	// strings.ToLower over every key below, which a fold table would
	// remove if it ever matters.
	termRunes := []rune(lowerTerm)
	scoredItems := make([]scored, 0, len(items))
	for _, it := range items {
		exactKey := key(it)
		lowerKey := strings.ToLower(exactKey)
		tier, ok := matchTier(lowerKey, lowerTerm, termRunes)
		if !ok {
			continue
		}
		scoredItems = append(scoredItems,
			scored{item: it, tier: tier, lower: lowerKey, exact: exactKey})
	}
	sort.SliceStable(scoredItems, func(i, j int) bool {
		if scoredItems[i].tier != scoredItems[j].tier {
			return scoredItems[i].tier < scoredItems[j].tier
		}
		if scoredItems[i].lower != scoredItems[j].lower {
			return scoredItems[i].lower < scoredItems[j].lower
		}
		return scoredItems[i].exact < scoredItems[j].exact
	})
	out := make([]T, len(scoredItems))
	for i, s := range scoredItems {
		out[i] = s.item
	}
	return out
}

// matchTier reports how lowerKey matches lowerTerm: 0 for a prefix
// match, 1 for a substring match, 2 for a subsequence match, ok=false
// for no match at all. lowerKey and lowerTerm must already be
// lowercased; termRunes is lowerTerm decoded once by the caller so
// isSubsequence never repeats that conversion per item.
func matchTier(lowerKey, lowerTerm string, termRunes []rune) (tier int, ok bool) {
	switch {
	case strings.HasPrefix(lowerKey, lowerTerm):
		return 0, true
	case strings.Contains(lowerKey, lowerTerm):
		return 1, true
	case isSubsequence(termRunes, lowerKey):
		return 2, true
	default:
		return 0, false
	}
}

// isSubsequence reports whether every rune of termRunes appears in s,
// in order, not necessarily adjacent. An empty termRunes is trivially
// true, though matchTier never reaches this tier for one: HasPrefix
// already matches an empty term at tier 0.
func isSubsequence(termRunes []rune, s string) bool {
	i := 0
	for _, r := range s {
		if i == len(termRunes) {
			break
		}
		if r == termRunes[i] {
			i++
		}
	}
	return i == len(termRunes)
}
