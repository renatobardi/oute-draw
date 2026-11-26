import { sceneCoordsToViewportCoords, viewportCoordsToSceneCoords } from "@excalidraw/excalidraw";
import { useEffect, useMemo, useRef, useState } from "react";

import { ApiError, timeAgo, type Comment, type SceneApi } from "../api";

/** The part of the canvas state that decides where a scene point is on screen. */
export interface Viewport {
  scrollX: number;
  scrollY: number;
  zoom: { value: number };
  offsetLeft: number;
  offsetTop: number;
}

interface Props {
  comments: Comment[];
  sceneApi: SceneApi;
  viewport: Viewport;
  /** While true, a click on the canvas drops a new comment there. */
  placing: boolean;
  onPlaced: () => void;
  onChanged: () => void;
  onError: (message: string) => void;
  /** Member id, or null for a guest; decides which comments can be deleted. */
  userId: string | null;
  isAdmin: boolean;
  openThread: string | null;
  setOpenThread: (id: string | null) => void;
}

const initials = (name: string) =>
  name
    .split(/\s+/)
    .map((p) => p[0])
    .slice(0, 2)
    .join("")
    .toUpperCase() || "?";

export const threadsOf = (comments: Comment[]) => {
  const roots = comments.filter((c) => !c.threadId);
  return roots.map((root) => ({ root, replies: comments.filter((c) => c.threadId === root.id) }));
};

/** Pins over the canvas, the composer for a new comment and the open thread. */
export const CommentLayer = (p: Props) => {
  const [draft, setDraft] = useState<{ x: number; y: number } | null>(null);
  const threads = useMemo(() => threadsOf(p.comments), [p.comments]);
  const vp = p.viewport as Parameters<typeof sceneCoordsToViewportCoords>[1];

  const toScreen = (x: number, y: number) => sceneCoordsToViewportCoords({ sceneX: x, sceneY: y }, vp);
  const run = async (fn: () => Promise<unknown>) => {
    try {
      await fn();
      p.onChanged();
    } catch (e) {
      p.onError(e instanceof ApiError ? e.message : "Não foi possível salvar o comentário.");
    }
  };

  useEffect(() => {
    if (!p.placing) setDraft(null);
  }, [p.placing]);

  const open = threads.find((t) => t.root.id === p.openThread);

  return (
    <>
      {p.placing && !draft && (
        <div
          className="comment-catcher"
          title="Clique onde quer comentar"
          onClick={(e) => {
            const pt = viewportCoordsToSceneCoords({ clientX: e.clientX, clientY: e.clientY }, vp);
            p.setOpenThread(null);
            setDraft(pt);
          }}
        />
      )}
      {threads
        .filter((t) => !t.root.resolvedAt || t.root.id === p.openThread)
        .map((t, i) => {
          const at = toScreen(t.root.x, t.root.y);
          return (
            <button
              key={t.root.id}
              className={`pin${t.root.resolvedAt ? " resolved" : ""}${t.root.id === p.openThread ? " active" : ""}`}
              style={{ left: at.x - p.viewport.offsetLeft, top: at.y - p.viewport.offsetTop }}
              title={`${t.root.authorName}: ${t.root.body}`}
              aria-label={`Comentário ${i + 1} de ${t.root.authorName}`}
              onClick={() => {
                setDraft(null);
                p.setOpenThread(t.root.id === p.openThread ? null : t.root.id);
              }}
            >
              {initials(t.root.authorName)}
            </button>
          );
        })}
      {draft && (
        <Popover at={toScreen(draft.x, draft.y)}>
          <Composer
            placeholder="Escreva um comentário"
            submit="Comentar"
            autoFocus
            onCancel={() => {
              setDraft(null);
              p.onPlaced();
            }}
            onSubmit={(body) =>
              run(async () => {
                const c = await p.sceneApi.addComment({ body, x: draft.x, y: draft.y });
                setDraft(null);
                p.onPlaced();
                p.setOpenThread(c.id);
              })
            }
          />
        </Popover>
      )}
      {open && (
        <Popover at={toScreen(open.root.x, open.root.y)}>
          <div className="thread">
            {[open.root, ...open.replies].map((c) => (
              <div key={c.id} className="comment">
                <div className="comment-head">
                  <strong className="ellipsis">{c.authorName}</strong>
                  <span className="muted small">{timeAgo(c.createdAt)}</span>
                  <span className="grow" />
                  {(p.isAdmin || (p.userId && c.authorId === p.userId)) && (
                    <button
                      className="icon-btn small"
                      title="Excluir comentário"
                      onClick={() => {
                        if (confirm(c.threadId ? "Excluir esta resposta?" : "Excluir a conversa inteira?")) {
                          void run(() => p.sceneApi.deleteComment(c.id));
                          if (!c.threadId) p.setOpenThread(null);
                        }
                      }}
                    >
                      ×
                    </button>
                  )}
                </div>
                <p>{c.body}</p>
              </div>
            ))}
          </div>
          <Composer
            placeholder="Responder"
            submit="Responder"
            onSubmit={(body) => run(() => p.sceneApi.addComment({ body, threadId: open.root.id }))}
          />
          <div className="row">
            <button
              className="btn ghost small"
              onClick={() => {
                void run(() => p.sceneApi.patchComment(open.root.id, { resolved: !open.root.resolvedAt }));
                if (!open.root.resolvedAt) p.setOpenThread(null);
              }}
            >
              {open.root.resolvedAt ? "Reabrir" : "Resolver"}
            </button>
            <span className="grow" />
            <button className="btn ghost small" onClick={() => p.setOpenThread(null)}>
              Fechar
            </button>
          </div>
        </Popover>
      )}
    </>
  );
};

/** Keeps a floating box next to a screen point without letting it leave the window. */
const Popover = ({ at, children }: { at: { x: number; y: number }; children: React.ReactNode }) => {
  const width = Math.min(300, window.innerWidth - 24);
  const left = Math.max(12, Math.min(at.x + 18, window.innerWidth - width - 12));
  const top = Math.max(12, Math.min(at.y - 10, window.innerHeight - 260));
  return (
    <div className="popover" style={{ left, top, width }} onPointerDown={(e) => e.stopPropagation()}>
      {children}
    </div>
  );
};

const Composer = (props: {
  placeholder: string;
  submit: string;
  autoFocus?: boolean;
  onSubmit: (body: string) => Promise<void> | void;
  onCancel?: () => void;
}) => {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (props.autoFocus) ref.current?.focus();
  }, [props.autoFocus]);
  const send = async () => {
    const body = text.trim();
    if (!body || busy) return;
    setBusy(true);
    await props.onSubmit(body);
    setBusy(false);
    setText("");
  };
  return (
    <div className="composer">
      <textarea
        ref={ref}
        rows={2}
        value={text}
        placeholder={props.placeholder}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          // The canvas listens for keys globally; typing here must not draw.
          e.stopPropagation();
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void send();
          if (e.key === "Escape") props.onCancel?.();
        }}
      />
      <div className="row">
        {props.onCancel && (
          <button className="btn ghost small" onClick={props.onCancel}>
            Cancelar
          </button>
        )}
        <span className="grow" />
        <button className="btn primary small" disabled={!text.trim() || busy} onClick={() => void send()}>
          {props.submit}
        </button>
      </div>
    </div>
  );
};

/** The side panel: every conversation of the scene, open ones first. */
export const CommentsPanel = (props: {
  comments: Comment[];
  canComment: boolean;
  placing: boolean;
  onPlace: () => void;
  onFocus: (c: Comment) => void;
}) => {
  const [showResolved, setShowResolved] = useState(false);
  const threads = threadsOf(props.comments);
  const visible = threads.filter((t) => showResolved || !t.root.resolvedAt);
  const resolved = threads.length - threads.filter((t) => !t.root.resolvedAt).length;
  return (
    <>
      {props.canComment && (
        <button className={`btn${props.placing ? " primary" : ""}`} onClick={props.onPlace}>
          {props.placing ? "Clique no desenho…" : "Novo comentário"}
        </button>
      )}
      {visible.length === 0 && <p className="muted">Nenhum comentário aberto.</p>}
      <div className="list">
        {visible.map((t) => (
          <button key={t.root.id} className="list-item" onClick={() => props.onFocus(t.root)}>
            <div className="row">
              <strong className="ellipsis">{t.root.authorName}</strong>
              <span className="muted small">{timeAgo(t.root.createdAt)}</span>
            </div>
            <div className={`clamp${t.root.resolvedAt ? " muted" : ""}`}>{t.root.body}</div>
            <div className="muted small">
              {t.replies.length > 0 && `${t.replies.length} resposta${t.replies.length > 1 ? "s" : ""}`}
              {t.root.resolvedAt && " · resolvido"}
            </div>
          </button>
        ))}
      </div>
      {resolved > 0 && (
        <label className="check">
          <input type="checkbox" checked={showResolved} onChange={(e) => setShowResolved(e.target.checked)} />
          Mostrar resolvidos ({resolved})
        </label>
      )}
    </>
  );
};
