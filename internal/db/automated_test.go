package db

import "testing"

func TestIsAutomatedSession(t *testing.T) {
	tests := []struct {
		name         string
		firstMessage string
		want         bool
	}{
		{
			"EmptyMessage",
			"",
			false,
		},
		{
			"NormalUserPrompt",
			"fix the login bug",
			false,
		},
		{
			"RoborevReviewPrompt",
			"You are a code reviewer. Review the code changes shown below.\n\n## Changes\n...",
			true,
		},
		{
			"RoborevReviewPromptExact",
			"You are a code reviewer. Review the code changes shown below.",
			true,
		},
		{
			"RoborevFixPromptWithBody",
			"# Fix Request\n\nAn analysis was performed and produced the following findings:\n...",
			true,
		},
		{
			"RoborevFixPromptExact",
			"# Fix Request",
			true,
		},
		{
			"SimilarButNotReview",
			"You are a code reviewer but I need help",
			false,
		},
		{
			"FixInNormalContext",
			"Fix the request handler",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsAutomatedSession(tt.firstMessage)
			if got != tt.want {
				t.Errorf(
					"IsAutomatedSession(%q) = %v, want %v",
					tt.firstMessage, got, tt.want,
				)
			}
		})
	}
}
