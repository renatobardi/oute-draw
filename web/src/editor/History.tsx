import { exportToSvg, restoreElements } from "@excalidraw/excalidraw";
import type { ExcalidrawElement } from "@excalidraw/excalidraw/element/types";
import type { BinaryFiles } from "@excalidraw/excalidraw/types";
import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, api, type Version } from "../api";

interface Props {
  sceneId: string;
  canEdit: boolean;
  files: () => BinaryFiles;
  /** Called after a restore so the editor pulls the restored scene. */
  onRestored: () => void;
  /** Saves pending edits first, so "save version" captures what is on screen. */
  flush: () => Promise<void>;
  onError: (message: string) => void;
}

const when = (iso: string) =>
  new Date(iso).toLocaleString("pt-BR", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" });

export const HistoryPanel = (p: Props) => {
  const [versions, setVersions] = useState<Version[] | null>(null);
  const [preview, setPreview] = useState<Version | null>(null);
  const fail = (e: unknown) => p.onError(e instanceof ApiError ? e.message : "Algo deu errado.");

  const load = useCallback(
    () => api.versions(p.sceneId).then(setVersions).catch(fail),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [p.sceneId],
  );
  useEffect(() => {
    void load();
  }, [load]);

  const saveVersion = async () => {
    const label = prompt("Nome desta versão", `Versão de ${when(new Date().toISOString())}`);
    if (!label?.trim()) return;
    try {
      await p.flush();
      await api.createVersion(p.sceneId, label);
      await load();
    } catch (e) {
      fail(e);
    }
  };

  const restore = async (v: Version) => {
    if (!confirm(`Restaurar "${v.label || when(v.createdAt)}"? O estado atual fica guardado no histórico.`)) return;
    try {
      await p.flush();
      await api.restoreVersion(p.sceneId, v.id);
      setPreview(null);
      p.onRestored();
      await load();
    } catch (e) {
      fail(e);
    }
  };

  return (
    <>
      {p.canEdit && (
        <button className="btn" onClick={() => void saveVersion()}>
          Salvar versão agora
        </button>
      )}
      <p className="muted small">
        Além das versões que você salva, um ponto automático é guardado no início de cada período de edição.
      </p>
      {versions === null ? (
        <p className="muted">Carregando…</p>
      ) : versions.length === 0 ? (
        <p className="muted">Nenhuma versão ainda.</p>
      ) : (
        <div className="list">
          {versions.map((v) => (
            <div key={v.id} className="list-item static">
              <div className="row">
                <strong className="ellipsis">{v.auto ? "Ponto automático" : v.label}</strong>
              </div>
              <div className="muted small">
                {when(v.createdAt)}
                {v.authorName && ` · ${v.authorName}`}
              </div>
              <div className="row">
                <button className="btn ghost small" onClick={() => setPreview(v)}>
                  Ver
                </button>
                {p.canEdit && (
                  <>
                    <button className="btn ghost small" onClick={() => void restore(v)}>
                      Restaurar
                    </button>
                    <span className="grow" />
                    <button
                      className="icon-btn small"
                      title="Excluir versão"
                      onClick={() => {
                        if (confirm("Excluir esta versão do histórico?")) {
                          api.deleteVersion(p.sceneId, v.id).then(load).catch(fail);
                        }
                      }}
                    >
                      ×
                    </button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
      {preview && (
        <VersionPreview
          sceneId={p.sceneId}
          version={preview}
          files={p.files}
          canRestore={p.canEdit}
          onRestore={() => void restore(preview)}
          onClose={() => setPreview(null)}
          onError={fail}
        />
      )}
    </>
  );
};

const VersionPreview = (p: {
  sceneId: string;
  version: Version;
  files: () => BinaryFiles;
  canRestore: boolean;
  onRestore: () => void;
  onClose: () => void;
  onError: (e: unknown) => void;
}) => {
  const box = useRef<HTMLDivElement>(null);
  const [empty, setEmpty] = useState(false);
  useEffect(() => {
    let live = true;
    api
      .version(p.sceneId, p.version.id)
      .then(async (v) => {
        const elements = restoreElements(v.elements as ExcalidrawElement[], null).filter((el) => !el.isDeleted);
        if (!elements.length) {
          setEmpty(true);
          return;
        }
        const svg = await exportToSvg({
          elements,
          appState: { ...v.appState, exportBackground: true },
          files: p.files(),
          exportPadding: 24,
        });
        if (live && box.current) {
          svg.removeAttribute("width");
          svg.removeAttribute("height");
          box.current.replaceChildren(svg);
        }
      })
      .catch(p.onError);
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [p.sceneId, p.version.id]);
  return (
    <div className="modal-back" onClick={p.onClose}>
      <div className="modal wide" role="dialog" aria-label="Prévia da versão" onClick={(e) => e.stopPropagation()}>
        <div className="row">
          <h2 className="ellipsis">{p.version.auto ? "Ponto automático" : p.version.label}</h2>
          <span className="muted small">{when(p.version.createdAt)}</span>
          <span className="grow" />
          {p.canRestore && (
            <button className="btn primary" onClick={p.onRestore}>
              Restaurar esta versão
            </button>
          )}
          <button className="btn ghost" onClick={p.onClose}>
            Fechar
          </button>
        </div>
        <div className="preview" ref={box}>
          <span className="muted">{empty ? "Esta versão estava vazia." : "Carregando…"}</span>
        </div>
      </div>
    </div>
  );
};
