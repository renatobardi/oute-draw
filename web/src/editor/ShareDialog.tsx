import { useCallback, useEffect, useState } from "react";

import { ApiError, api, type ShareLink } from "../api";

const expiryOptions = [
  { label: "Nunca expira", days: 0 },
  { label: "Expira em 1 dia", days: 1 },
  { label: "Expira em 7 dias", days: 7 },
  { label: "Expira em 30 dias", days: 30 },
];

export const linkUrl = (l: ShareLink) => `${location.origin}/s/${l.token}`;

export const ShareDialog = (p: { sceneId: string; canEdit: boolean; onClose: () => void }) => {
  const [links, setLinks] = useState<ShareLink[] | null>(null);
  const [mode, setMode] = useState<"view" | "edit">("view");
  const [password, setPassword] = useState("");
  const [days, setDays] = useState(0);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const fail = (e: unknown) => setError(e instanceof ApiError ? e.message : "Algo deu errado.");

  const load = useCallback(() => api.shares(p.sceneId).then(setLinks).catch(fail), [p.sceneId]);
  useEffect(() => {
    void load();
  }, [load]);

  const create = async () => {
    try {
      await api.createShare(p.sceneId, {
        mode,
        password: password || undefined,
        expiresAt: days ? new Date(Date.now() + days * 86400_000).toISOString() : null,
      });
      setPassword("");
      setError("");
      await load();
    } catch (e) {
      fail(e);
    }
  };

  const copy = async (key: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(key);
      setTimeout(() => setCopied(""), 1500);
    } catch {
      prompt("Copie o link:", text);
    }
  };

  return (
    <div className="modal-back" onClick={p.onClose}>
      <div className="modal" role="dialog" aria-label="Compartilhar" onClick={(e) => e.stopPropagation()}>
        <div className="row">
          <h2>Compartilhar</h2>
          <span className="grow" />
          <button className="icon-btn" aria-label="Fechar" onClick={p.onClose}>
            ×
          </button>
        </div>
        <p className="muted small">
          Quem tem login no workspace já abre este desenho pelo endereço da página. Os links abaixo dão acesso a quem
          não tem login, sem precisar de conta.
        </p>
        {error && <div className="banner error">{error}</div>}

        {p.canEdit && (
          <div className="share-new">
            <select value={mode} onChange={(e) => setMode(e.target.value as "view" | "edit")} aria-label="Permissão">
              <option value="view">Somente leitura</option>
              <option value="edit">Pode editar e comentar</option>
            </select>
            <select value={days} onChange={(e) => setDays(Number(e.target.value))} aria-label="Validade">
              {expiryOptions.map((o) => (
                <option key={o.days} value={o.days}>
                  {o.label}
                </option>
              ))}
            </select>
            <input
              type="text"
              autoComplete="off"
              placeholder="Senha (opcional)"
              value={password}
              maxLength={72}
              onChange={(e) => setPassword(e.target.value)}
            />
            <button className="btn primary" onClick={() => void create()}>
              Criar link
            </button>
          </div>
        )}

        {links === null ? (
          <p className="muted">Carregando…</p>
        ) : links.length === 0 ? (
          <p className="muted">Nenhum link criado.</p>
        ) : (
          <div className="list">
            {links.map((l) => (
              <div key={l.id} className="list-item static">
                <div className="row">
                  <strong>{l.mode === "edit" ? "Pode editar" : "Somente leitura"}</strong>
                  <span className="muted small">
                    {l.hasPassword && "com senha · "}
                    {l.expiresAt ? `expira ${new Date(l.expiresAt).toLocaleDateString("pt-BR")}` : "não expira"}
                  </span>
                </div>
                <input className="mono" readOnly value={linkUrl(l)} onFocus={(e) => e.target.select()} />
                <div className="row wrap">
                  <button className="btn ghost small" onClick={() => void copy(l.id, linkUrl(l))}>
                    {copied === l.id ? "Copiado" : "Copiar link"}
                  </button>
                  <button className="btn ghost small" onClick={() => void copy(`${l.id}p`, `${linkUrl(l)}?present=1`)}>
                    {copied === `${l.id}p` ? "Copiado" : "Link de apresentação"}
                  </button>
                  <button
                    className="btn ghost small"
                    onClick={() =>
                      void copy(
                        `${l.id}e`,
                        `<iframe src="${linkUrl(l)}?embed=1" width="100%" height="600" style="border:0" allowfullscreen></iframe>`,
                      )
                    }
                  >
                    {copied === `${l.id}e` ? "Copiado" : "Código de incorporação"}
                  </button>
                  <span className="grow" />
                  {p.canEdit && (
                    <button
                      className="btn ghost small danger"
                      onClick={() => {
                        if (confirm("Revogar este link? Quem o tiver perde o acesso na hora.")) {
                          api.deleteShare(p.sceneId, l.id).then(load).catch(fail);
                        }
                      }}
                    >
                      Revogar
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};
