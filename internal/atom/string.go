package atom

import (
	"strings"
)

// MaskKey masks an API key for logging, showing only the last 4 characters
func MaskKey(key string) string {
	if len(key) <= 4 {
		return key
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}

// IsQuestion checks if the given text appears to be a question
func IsQuestion(text string) bool {
	questionWords := []string{"what", "why", "how", "when", "where", "who", "which", "is", "are", "do", "does", "can", "could", "would", "should"}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "?") {
		return true
	}
	for _, w := range questionWords {
		if strings.HasPrefix(lower, w+" ") || strings.Contains(lower, w+" ") {
			return true
		}
	}
	return false
}

// StripMarkdownCodeBlock removes markdown code block delimiters from text
func StripMarkdownCodeBlock(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}
	return text
}
