package services

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const workspaceFileNameMatchBonus = 1000

func sortWorkspaceFileEntries(entries []WorkspaceFileEntry, query string) {
	type scoredEntry struct {
		entry WorkspaceFileEntry
		score int
	}
	scored := make([]scoredEntry, len(entries))
	for i, entry := range entries {
		score, _ := workspaceSearchScore(query, entry.Name, entry.Path)
		scored[i] = scoredEntry{entry: entry, score: score}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		left := scored[i]
		right := scored[j]
		if left.score != right.score {
			return left.score > right.score
		}
		if left.entry.Kind != right.entry.Kind {
			return left.entry.Kind == "directory"
		}
		return strings.ToLower(left.entry.Path) < strings.ToLower(right.entry.Path)
	})
	for i := range scored {
		entries[i] = scored[i].entry
	}
}

func workspaceSearchScore(query string, name string, relativePath string) (int, bool) {
	query = normalizeWorkspaceFuzzyText(query)
	if query == "" {
		return 0, true
	}

	nameScore, nameMatched := workspaceFuzzyScore(query, name)
	pathScore, pathMatched := workspaceFuzzyScore(query, filepath.ToSlash(relativePath))
	if !nameMatched && !pathMatched {
		return 0, false
	}
	if nameMatched {
		nameScore += workspaceFileNameMatchBonus
		if pathMatched && pathScore > 0 {
			nameScore += min(pathScore, 100)
		}
		return nameScore, true
	}
	return pathScore, true
}

func workspaceFuzzyScore(query string, candidate string) (int, bool) {
	query = normalizeWorkspaceFuzzyText(query)
	candidate = strings.TrimSpace(strings.ReplaceAll(candidate, "\\", "/"))
	if query == "" {
		return 0, true
	}

	queryRunes := []rune(query)
	candidateRunes := []rune(candidate)
	candidateLower := []rune(strings.ToLower(candidate))
	if len(queryRunes) > len(candidateLower) {
		return 0, false
	}

	const noMatch = -1 << 30
	previous := make([]int, len(candidateRunes))
	for i := range previous {
		previous[i] = noMatch
		if candidateLower[i] != queryRunes[0] {
			continue
		}
		previous[i] = 10 + workspaceFuzzyBoundaryBonus(candidateRunes, i) + max(0, 20-i) - i
	}

	for queryIndex := 1; queryIndex < len(queryRunes); queryIndex++ {
		current := make([]int, len(candidateRunes))
		prefixBest := make([]int, len(candidateRunes))
		bestPrevious := noMatch
		for i, previousScore := range previous {
			bestPrevious = max(bestPrevious, previousScore)
			prefixBest[i] = bestPrevious
		}
		for i := range current {
			current[i] = noMatch
			if candidateLower[i] != queryRunes[queryIndex] {
				continue
			}
			characterScore := 10 + workspaceFuzzyBoundaryBonus(candidateRunes, i)
			bestTransition := noMatch
			if i > 0 && previous[i-1] != noMatch {
				bestTransition = previous[i-1] + 24
			}
			for previousIndex := max(0, i-21); previousIndex <= i-2; previousIndex++ {
				if previous[previousIndex] == noMatch {
					continue
				}
				gap := i - previousIndex - 1
				bestTransition = max(bestTransition, previous[previousIndex]-gap)
			}
			if distantIndex := i - 22; distantIndex >= 0 && prefixBest[distantIndex] != noMatch {
				bestTransition = max(bestTransition, prefixBest[distantIndex]-20)
			}
			if bestTransition != noMatch {
				current[i] = bestTransition + characterScore
			}
		}
		previous = current
	}

	score := noMatch
	for _, candidateScore := range previous {
		score = max(score, candidateScore)
	}
	if score == noMatch {
		return 0, false
	}

	candidateNormalized := normalizeWorkspaceFuzzyText(candidate)
	switch {
	case candidateNormalized == query:
		score += 500
	case strings.HasPrefix(candidateNormalized, query):
		score += 300
	case strings.Contains(candidateNormalized, query):
		score += 180
	}
	score -= max(0, len(candidateRunes)-len(queryRunes)) / 2
	return score, true
}

func workspaceFuzzyBoundaryBonus(candidate []rune, index int) int {
	if index == 0 {
		return 35
	}
	previous := candidate[index-1]
	current := candidate[index]
	if strings.ContainsRune("/\\_- .", previous) {
		return 30
	}
	if unicode.IsLower(previous) && unicode.IsUpper(current) {
		return 20
	}
	return 0
}

func normalizeWorkspaceFuzzyText(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "\\", "/")))
}
