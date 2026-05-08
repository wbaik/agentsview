package parser

import "encoding/json"

// MemoryCitationJSON serializes a parsed citation with stable empty arrays.
func MemoryCitationJSON(citation *ParsedMemoryCitation) string {
	if citation == nil {
		return ""
	}
	normalized := ParsedMemoryCitation{
		Entries:    citation.Entries,
		RolloutIDs: citation.RolloutIDs,
	}
	if normalized.Entries == nil {
		normalized.Entries = []ParsedMemoryCitationEntry{}
	}
	if normalized.RolloutIDs == nil {
		normalized.RolloutIDs = []string{}
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	return string(b)
}
