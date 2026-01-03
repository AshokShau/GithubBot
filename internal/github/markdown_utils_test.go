package github

import (
	"strings"
	"testing"
)

func TestEscapeMarkdownV2(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			input:    "Hello_World",
			expected: "Hello\\_World",
		},
		{
			input:    "[]()~`>#+-=|{}.!",
			expected: "\\[\\]\\(\\)\\~\\`\\>\\#\\+\\-\\=\\|\\{\\}\\.\\!",
		},
		{
			input:    "Backslash \\ test",
			expected: "Backslash \\\\ test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := EscapeMarkdownV2(tt.input); got != tt.expected {
				t.Errorf("EscapeMarkdownV2() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEscapeMarkdownV2URL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "https://example.com",
			expected: "https://example.com",
		},
		{
			input:    "https://example.com/foo(bar)",
			expected: "https://example.com/foo\\(bar\\)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := EscapeMarkdownV2URL(tt.input); got != tt.expected {
				t.Errorf("EscapeMarkdownV2URL() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFormatRepo(t *testing.T) {
	tests := []struct {
		repo     string
		expected string
	}{
		{
			repo:     "owner/repo",
			expected: "[owner/repo](https://github.com/owner/repo)",
		},
		{
			repo:     "owner/my_repo",
			expected: "[owner/my\\_repo](https://github.com/owner/my_repo)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			if got := FormatRepo(tt.repo); got != tt.expected {
				t.Errorf("FormatRepo() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFormatUser(t *testing.T) {
	tests := []struct {
		user     string
		expected string
	}{
		{
			user:     "octocat",
			expected: "[octocat](https://github.com/octocat)",
		},
		{
			user:     "user_name",
			expected: "[user\\_name](https://github.com/user_name)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			if got := FormatUser(tt.user); got != tt.expected {
				t.Errorf("FormatUser() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestFormatReleaseBody(t *testing.T) {
	t.Run("Short body", func(t *testing.T) {
		body := "Short release notes"
		got := FormatReleaseBody(body)
		if strings.Contains(got, "||") {
			t.Errorf("FormatReleaseBody() unexpected spoiler tag for short body")
		}
		if !strings.HasPrefix(got, ">") {
			t.Errorf("FormatReleaseBody() expected blockquote prefix")
		}
	})

	t.Run("Long body", func(t *testing.T) {
		// Create a body with more than 10 lines
		var lines []string
		for i := 0; i < 15; i++ {
			lines = append(lines, "line")
		}
		body := strings.Join(lines, "\n")
		got := FormatReleaseBody(body)

		if !strings.HasSuffix(got, "||") {
			t.Errorf("FormatReleaseBody() expected spoiler tag at end for long body")
		}
		if !strings.Contains(got, "||\n>") {
			t.Errorf("FormatReleaseBody() expected spoiler tag in middle")
		}
	})
}
