# Homebrew Cask Dağıtımı

Agent Chat, `brew install --cask` ile kurulur ve `brew upgrade --cask` ile güncellenir.
Release akışı zaten imzalı + notarize bir universal DMG üretiyor; cask yalnızca o DMG'yi
işaret eder. Uygulama kodunda karşılığı yoktur.

## Parçalar

| Dosya | Görev |
|-------|-------|
| `agent-chat.rb.tmpl` | Cask'ın tek kaynağı. `__VERSION__` / `__SHA256__` yer tutucuları render sırasında doldurulur. |
| `render-cask.sh` | Yer tutucuları doldurur. sha256 verilmezse DMG'yi cask'ın işaret ettiği URL'den indirip hesaplar. |
| `.github/workflows/release.yml` → `bump-cask` job'ı | Her `v*` tag'inde render edip tap repo'suna commit'ler. |

CI ile lokal aynı script'i çağırır, yani lokalde denediğin çıktı CI'ın ürettiğiyle birebir aynıdır.

## Tek seferlik kurulum (bakımcı)

Bu ikisi yapılmadan `bump-cask` job'ı çalışmaz ve cask tap'e hiç düşmez.

**1. Tap repo'sunu oluştur: `mytsx/homebrew-agent-chat`**

Ad birebir bu olmalı — `brew`, `mytsx/agent-chat` tap'ini `homebrew-agent-chat`
repo'suna çevirir. Public olmalı.

> **Repo'yu boş bırakma.** GitHub'da "Add a README" işaretlenmeden açılan repo'nun hiç
> commit'i ve dolayısıyla default branch'i olmaz; `actions/checkout` bunu
> "couldn't find remote ref" ile reddeder. En az bir commit'le oluştur.

`Casks/agent-chat.rb`'yi elle eklemeye gerek yok — ilk `v*` tag'inde CI oluşturur.

**2. `TAP_REPO_TOKEN` secret'ını ekle**

Workflow'un varsayılan `GITHUB_TOKEN`'ı yalnız kendi repo'suna yazabilir, tap'e yazamaz.
Bu yüzden ayrı bir kimlik gerekir:

- `homebrew-agent-chat` üzerinde yazma yetkisi olan bir PAT üret (classic: `repo` scope;
  fine-grained: yalnız o repo + Contents: Read and write).
- `mytsx/agent-chat` → Settings → Secrets and variables → Actions → New repository secret,
  ad: `TAP_REPO_TOKEN`.

Secret eksikse `bump-cask` job'ı 403 yerine açık bir hata mesajıyla durur.

## Nasıl çalışıyor

1. `v*` tag'i → `build-and-release` job'ı DMG'yi üretip GitHub Release'e yükler.
2. `bump-cask` job'ı (`needs: build-and-release`) çalışır — release yayımlanmadan başlamaz,
   dolayısıyla asset henüz yokken hash alma yarışı oluşmaz.
3. `render-cask.sh`, DMG'yi **cask'ın kullanıcıya göstereceği URL'den** indirir ve hash'ler.
   Böylece hem checksum gerçek byte'ları tarif eder hem de URL'nin çözüldüğü kanıtlanır.
4. Cask, tap repo'suna commit'lenir. Kullanıcı `brew upgrade --cask` ile yeni sürümü alır.

Ön-sürümler (`v1.2.0-beta.1` gibi, tag'inde `-` geçenler) bump'lanmaz — kararlı kanaldaki
kullanıcılara beta düşmemesi için, release adımındaki `prerelease:` mantığıyla aynı koşul.

## Lokal doğrulama

`brew audit` artık dosya yoluyla çalışmıyor (`brew audit [path]` devre dışı), cask'ın bir
tap içinde olması gerekir:

```bash
brew tap-new mytsx/agent-chat --no-git
TAP="$(brew --repository)/Library/Taps/mytsx/homebrew-agent-chat"
mkdir -p "$TAP/Casks" && rm -rf "$TAP/.github"
./packaging/homebrew/render-cask.sh 0.5.0 > "$TAP/Casks/agent-chat.rb"

brew style mytsx/agent-chat/agent-chat
brew audit --cask mytsx/agent-chat/agent-chat
brew fetch --cask mytsx/agent-chat/agent-chat   # URL + sha256'yı gerçekten indirip doğrular

brew untap mytsx/agent-chat                     # temizlik
```

## Notlar

- **`depends_on macos: :monterey`** — çıplak sembol Homebrew'de "bu sürüm **ve üstü**"
  demektir (`DependsOn#macos=` varsayılan comparator'ı `">="`). `">= :monterey"` string
  formu `odeprecated` olduğu için kullanılmaz. Taban `go.mod`'dan gelir: Go 1.25 macOS 12
  Monterey'den eskisini desteklemiyor.
- **`auto_updates false`** — uygulama kendini güncellemiyor; güncellemeyi Homebrew yönetir.
- **Asset adı ile bundle adı farklıdır:** DMG `AgentChat-<version>-universal.dmg` (boşluksuz),
  uygulama bundle'ı `Agent Chat.app` (boşluklu). Cask'ta ikisi ayrı stanza'larda geçer.
