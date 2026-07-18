import { useUpdate } from "../store/useUpdate";

// Homebrew upgrade hint, kept consistent with the Cask distribution (#82).
const BREW_HINT = "brew upgrade --cask agent-chat";

// openExternal opens a URL in the user's default browser. It guards against an empty
// URL (a degenerate release with no html_url/dmg — the button is then inert rather
// than opening about:blank) and loads the Wails runtime dynamically, matching the
// rest of the app so runtime.js isn't pulled into the main bundle chunk.
function openExternal(url: string) {
  if (!url) return;
  import("../../wailsjs/runtime/runtime")
    .then(({ BrowserOpenURL }) => BrowserOpenURL(url))
    .catch((e) => {
      if (import.meta.env.DEV) console.warn("BrowserOpenURL failed:", e);
    });
}

// UpdateBanner is the non-intrusive, dismissable "yeni sürüm var" bar (#83). It only
// notifies and links out — the app never self-updates. Rendered null unless an update
// is available and not dismissed.
export default function UpdateBanner() {
  const info = useUpdate((s) => s.info);
  const dismissed = useUpdate((s) => s.dismissed);
  const dismiss = useUpdate((s) => s.dismiss);

  if (!info || dismissed) return null;

  // "İndir" prefers the direct .dmg; falls back to the release page when the release
  // has no dmg asset, so the button always leads somewhere useful.
  const downloadURL = info.dmgURL || info.releaseURL;

  return (
    <div className="update-notice" role="status">
      <span className="update-notice-text">
        🎉 <strong>v{info.version}</strong> yayınlandı
        {info.currentVersion ? ` (mevcut: v${info.currentVersion})` : ""} —{" "}
        <button
          type="button"
          className="update-link"
          onClick={() => openExternal(downloadURL)}
        >
          İndir
        </button>
        {" · "}
        <button
          type="button"
          className="update-link"
          onClick={() => openExternal(info.releaseURL)}
        >
          Notları gör
        </button>
        {" · Homebrew: "}
        <code>{BREW_HINT}</code>
      </span>
      <button
        type="button"
        className="update-dismiss"
        onClick={dismiss}
        title="Kapat"
        aria-label="Güncelleme bildirimini kapat"
      >
        ×
      </button>
    </div>
  );
}
