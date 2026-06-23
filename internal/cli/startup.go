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
//
// agentMode (#17) selects the join role and read instruction: "manager" (routing
// authority, reads all), "observer" (read-only outside eye — watches via
// read_all_messages but is told not to send), or "" (a normal agent). A single
// mode string is used rather than parallel bools so the manager/observer states
// can never both be set.
func ComposeStartupPrompt(basePrompt, globalPrompt, teamPrompt, roomSummary, selectedPrompt, agentName, agentRole, teamName, agentMode string) string {
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
	isManager := strings.EqualFold(strings.TrimSpace(agentMode), "manager")
	isObserver := strings.EqualFold(strings.TrimSpace(agentMode), "observer")

	role := strings.TrimSpace(agentRole)
	if role == "" {
		role = agentName
	}
	readInstruction := fmt.Sprintf("Odaya katıldıktan sonra read_messages(\"%s\") ile mesajları oku ve diğer agent'larla iletişime geç.", agentName)
	switch {
	case isManager:
		role = "manager"
		// read_all_messages varsayılan limit'i 15'tir; limit verilmezse manager
		// sessizce yalnızca son 15 mesajı görür. Tüm geçmişi (oda en çok 500 mesaj
		// tutar) okuması için limit'i açıkça yüksek geç.
		readInstruction = "Odaya katıldıktan sonra read_all_messages(since_id=0, limit=1000) ile tüm mesajları oku ve yönlendir."
	case isObserver:
		// Observer salt-okunur dış gözdür: rolü, serbest agentRole'ü EZER ki hub
		// onu tanıyıp send_message'ını reddetsin. Tüm trafiği read_all_messages ile
		// izler, ama hiçbir agent'a mesaj göndermez — yalnızca kullanıcıyla konuşur.
		role = "observer"
		// limit=1000 like the manager path: read_all_messages defaults to 15, so
		// without it an observer meant to watch "all traffic" would only see the
		// last 15 messages (oda en çok 500 mesaj tutar).
		readInstruction = "Odaya katıldıktan sonra read_all_messages(since_id=0, limit=1000) ile odadaki tüm trafiği izle. DİĞER AGENT'LARA MESAJ GÖNDERME (send_message reddedilir); yalnızca kullanıcıyla konuş ve odanın gidişatını analiz et."
	}
	if hasSummary {
		// Önceki session özeti zaten yukarıda enjekte edildi: agent'ı tüm geçmişi
		// (limit=1000) çekmek yerine özete yönlendir; ayrıntı gerekirse read_summary
		// ya da SINIRLI bir okuma yeter. Bu, özetin önlemek için var olduğu token
		// şişmesini engeller.
		switch {
		case isManager:
			readInstruction = "Önceki session özetini yukarıda okudun. Güncel ayrıntı için read_summary() çağır; yalnızca gerekiyorsa son mesajları read_all_messages(since_id=0, limit=50) ile çek ve yönlendir."
		case isObserver:
			readInstruction = "Önceki session özetini yukarıda okudun. Güncel trafiği read_all_messages(since_id=0, limit=1000) ile izle; DİĞER AGENT'LARA MESAJ GÖNDERME, yalnızca kullanıcıyla konuş."
		default:
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
