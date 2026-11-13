package tui

import "strings"

// matchQuery accepts literal substrings and ordered fuzzy character matches.
// Multiple terms must each match, so a path and title can be searched together.
func matchQuery(label, query string) bool {
	label = strings.ToLower(label)
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if strings.Contains(label, term) {
			continue
		}
		chars := []rune(label)
		wanted := []rune(term)
		at := 0
		for _, r := range chars {
			if at < len(wanted) && r == wanted[at] {
				at++
			}
		}
		if at != len(wanted) {
			return false
		}
	}
	return true
}
