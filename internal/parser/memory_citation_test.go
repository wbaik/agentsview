package parser

import "testing"

func TestMemoryCitationJSONNormalizesEmptyRolloutIDs(t *testing.T) {
	citation := &ParsedMemoryCitation{
		Entries: []ParsedMemoryCitationEntry{{
			Path:      "MEMORY.md",
			LineStart: 50,
			LineEnd:   103,
			Note:      "prior context",
		}},
	}

	got := MemoryCitationJSON(citation)
	want := `{"entries":[{"path":"MEMORY.md","lineStart":50,"lineEnd":103,"note":"prior context"}],"rolloutIds":[]}`
	if got != want {
		t.Fatalf("MemoryCitationJSON = %q, want %q", got, want)
	}
	if citation.RolloutIDs != nil {
		t.Fatalf("MemoryCitationJSON mutated RolloutIDs to %#v", citation.RolloutIDs)
	}
}
