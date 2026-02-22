package cli

import (
	"fmt"
	"strings"
)

// ComposeStartupPrompt builds the full startup prompt from multiple parts.
func ComposeStartupPrompt(basePrompt, globalPrompt, teamPrompt, selectedPrompt, agentName, teamName string, isManager bool) string {
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
	role := agentName
	readInstruction := fmt.Sprintf("Odaya katıldıktan sonra read_messages(\"%s\") ile mesajları oku ve diğer agent'larla iletişime geç.", agentName)
	if isManager {
		role = "manager"
		readInstruction = "Odaya katıldıktan sonra read_all_messages(since_id=0) ile tüm mesajları oku ve yönlendir."
	}

	joinInstruction := fmt.Sprintf(
		"Sen '%s' agent'ısın. '%s' takımındasın.\n"+
			"Hemen join_room(\"%s\", \"%s\") çağır ve odaya katıl.\n"+
			"%s\n"+
			"Tüm tool çağrılarında agent_name olarak her zaman \"%s\" kullan.",
		agentName, teamName,
		agentName, role,
		readInstruction,
		agentName,
	)
	parts = append(parts, joinInstruction)

	return strings.Join(parts, "\n\n")
}
