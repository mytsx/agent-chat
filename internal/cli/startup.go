package cli

import (
	"fmt"
	"strings"
)

// ComposeStartupPrompt builds the full startup prompt from multiple parts.
func ComposeStartupPrompt(basePrompt, globalPrompt, teamPrompt, selectedPrompt, agentName, teamName string) string {
	var parts []string

	// 1. Base prompt (always included)
	if basePrompt = strings.TrimSpace(basePrompt); basePrompt != "" {
		parts = append(parts, basePrompt)
	}

	// 2. Global custom prompt (optional)
	if globalPrompt = strings.TrimSpace(globalPrompt); globalPrompt != "" {
		parts = append(parts, globalPrompt)
	}

	// 3. Team custom prompt (optional)
	if teamPrompt = strings.TrimSpace(teamPrompt); teamPrompt != "" {
		parts = append(parts, teamPrompt)
	}

	// 4. Selected prompt from library (optional)
	if selectedPrompt = strings.TrimSpace(selectedPrompt); selectedPrompt != "" {
		parts = append(parts, selectedPrompt)
	}

	// 5. Join instruction (always included)
	joinInstruction := fmt.Sprintf(
		"Sen '%s' agent'ısın. '%s' takımındasın.\n"+
			"Hemen join_room(\"%s\", \"%s\") çağır ve odaya katıl.\n"+
			"Odaya katıldıktan sonra read_messages(\"%s\") ile mesajları oku ve diğer agent'larla iletişime geç.\n"+
			"Tüm tool çağrılarında agent_name olarak her zaman \"%s\" kullan.",
		agentName, teamName,
		agentName, agentName,
		agentName,
		agentName,
	)
	parts = append(parts, joinInstruction)

	return strings.Join(parts, "\n\n")
}
