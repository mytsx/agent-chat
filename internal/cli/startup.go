package cli

import (
	"fmt"
	"strings"
)

// roomSummaryHeader labels the prior-session summary segment so agents read it
// as background context, not as an instruction directed at them.
const roomSummaryHeader = "## Önceki Session Özeti (bağlam)"

// ComposeStartupPrompt builds the full startup prompt from multiple parts.
//
// Segment order: base → global → team charter → room summary (#29) → selected
// library prompt → join instruction. The room summary sits right after the
// charter (its own labeled segment, never overwriting the charter) so a
// continuing agent inherits prior-session context. When a summary is present the
// join instruction steers agents to read_summary instead of pulling the whole
// history, avoiding the token bloat the summary exists to prevent.
func ComposeStartupPrompt(basePrompt, globalPrompt, teamPrompt, roomSummary, selectedPrompt, agentName, agentRole, teamName string, isManager bool) string {
	var parts []string

	// 1. Base prompt (always included)
	if basePrompt = strings.TrimSpace(basePrompt); basePrompt != "" {
		parts = append(parts, basePrompt)
	}

	// 2. Global custom prompt (optional)
	if globalPrompt = strings.TrimSpace(globalPrompt); globalPrompt != "" {
		parts = append(parts, globalPrompt)
	}

	// 3. Team custom prompt / charter (optional)
	if teamPrompt = strings.TrimSpace(teamPrompt); teamPrompt != "" {
		parts = append(parts, teamPrompt)
	}

	// 4. Room summary from the previous session (#29, optional). Its own segment
	//    after the charter; never merged into or overriding the charter.
	hasSummary := strings.TrimSpace(roomSummary) != ""
	if hasSummary {
		parts = append(parts, roomSummaryHeader+"\n"+strings.TrimSpace(roomSummary))
	}

	// 5. Selected prompt from library (optional)
	if selectedPrompt = strings.TrimSpace(selectedPrompt); selectedPrompt != "" {
		parts = append(parts, selectedPrompt)
	}

	// 6. Join instruction (always included)
	role := strings.TrimSpace(agentRole)
	if role == "" {
		role = agentName
	}
	readInstruction := fmt.Sprintf("Odaya katıldıktan sonra read_messages(\"%s\") ile mesajları oku ve diğer agent'larla iletişime geç.", agentName)
	if isManager {
		role = "manager"
		// read_all_messages varsayılan limit'i 15'tir; limit verilmezse manager
		// sessizce yalnızca son 15 mesajı görür. Tüm geçmişi (oda en çok 500 mesaj
		// tutar) okuması için limit'i açıkça yüksek geç.
		readInstruction = "Odaya katıldıktan sonra read_all_messages(since_id=0, limit=1000) ile tüm mesajları oku ve yönlendir."
	}
	if hasSummary {
		// Önceki session özeti zaten yukarıda enjekte edildi: agent'ı tüm geçmişi
		// (limit=1000) çekmek yerine özete yönlendir; ayrıntı gerekirse read_summary
		// ya da SINIRLI bir okuma yeter. Bu, özetin önlemek için var olduğu token
		// şişmesini engeller.
		if isManager {
			readInstruction = "Önceki session özetini yukarıda okudun. Güncel ayrıntı için read_summary() çağır; yalnızca gerekiyorsa son mesajları read_all_messages(since_id=0, limit=50) ile çek ve yönlendir."
		} else {
			readInstruction = fmt.Sprintf("Önceki session özetini yukarıda okudun. Ayrıntı için read_summary() ya da read_messages(\"%s\") çağır ve diğer agent'larla iletişime geç.", agentName)
		}
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
