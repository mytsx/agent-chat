# TODO

Bilinen sorunlar ve yapilacaklar listesi.

## Bugs

- [ ] **Resim paste edilemiyor** — Terminal panellerine veya chat alanina resim yapistirildiginda (Cmd+V) resim icerigi islenmez/goruntulenmez. xterm.js varsayilan olarak resim paste destegi sunmaz; ozel bir handler veya binary paste destegi gerekebilir.

## Planned Features

### Session Persistence & Terminal Restart (CLI Resume)

CLI session'larini yakalayip saklama ve terminal restart/resume destegi.

**Referans repo:** https://github.com/yigitkonur/cli-continues (332 star, TypeScript)
- Tek komutla AI CLI session'larini resume ediyor
- 7 CLI destegi: Claude Code, Gemini CLI, Copilot, Codex, OpenCode, Factory Droid, Cursor CLI
- Native resume (ayni CLI'da `--resume`) ve cross-tool resume (farkli CLI'ya gecis) destegi

**Session dosya konumlari (cli-continues'den ogrenilenler):**

| CLI | Konum | Format |
|-----|-------|--------|
| Claude Code | `~/.claude/projects/{project-hash}/{uuid}.jsonl` | JSONL — ilk 50 satirda `sessionId` alani |
| Gemini CLI | `~/.gemini/tmp/{hash}/chats/session-*.json` | JSON — `sessionId` alani |
| Copilot | `~/.copilot/session-state/` | YAML |

**Yapilacaklar:**

- [ ] PTY baslatildiktan sonra CLI'in session dosyasini tarayip `sessionId` yakalama
- [ ] `TerminalSession` struct'ina `cliSessionID` alani ekleme
- [ ] `restartTerminal` fonksiyonuna `--resume {sessionId}` destegi (Claude: `claude --resume`, Gemini: TBD)
- [ ] Uygulama kapaninca team + session bilgilerini diske yazma (`~/.agent-chat/state.json`)
- [ ] Uygulama acilinca onceki session'lari restore etme secenegi
- [ ] Cross-tool resume: bir CLI'dan digerine baglam aktarimi (cli-continues'un markdown handoff yaklasimiyla)

**Entegrasyon secenekleri:**
1. **Go'ya port etme (onerilen):** Session kesif ve resume mantigini `internal/cli/` paketine Go ile entegre etme — dis bagimllik yok
2. **Direkt kullanim:** `npx continues resume {id}` komutu — Node.js 22 bagimlilik getirir

---

Detayli yol haritasi icin bkz. [ROADMAP.md](./ROADMAP.md)
