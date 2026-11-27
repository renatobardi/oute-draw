import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, api, timeAgo, type Collection, type Me, type Role, type SceneMeta, type User } from "../api";
import { navigate } from "../nav";

type View = { kind: "all" } | { kind: "none" } | { kind: "collection"; id: string } | { kind: "trash" } | { kind: "members" };

const viewKey = (v: View) => (v.kind === "collection" ? v.id : v.kind);

export const Dashboard = () => {
  const [me, setMe] = useState<Me | null>(null);
  const [collections, setCollections] = useState<Collection[]>([]);
  const [scenes, setScenes] = useState<SceneMeta[] | null>(null);
  const [view, setView] = useState<View>({ kind: "all" });
  const [query, setQuery] = useState("");
  const [error, setError] = useState("");
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const [navOpen, setNavOpen] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  const canEdit = me ? me.user.role !== "viewer" : false;
  const fail = (e: unknown) => setError(e instanceof ApiError ? e.message : "Algo deu errado.");

  const loadCollections = useCallback(() => api.collections().then(setCollections).catch(fail), []);
  const loadScenes = useCallback(() => {
    if (view.kind === "members") return;
    const collection = view.kind === "collection" ? view.id : view.kind === "none" ? "none" : "";
    api
      .scenes({ collection, q: query.trim(), trash: view.kind === "trash" })
      .then(setScenes)
      .catch(fail);
  }, [view, query]);

  useEffect(() => {
    document.title = "oute draw";
    api.me().then(setMe).catch(fail);
    loadCollections();
  }, [loadCollections]);

  // Typing in the search box must not fire a request per keystroke.
  useEffect(() => {
    const t = setTimeout(loadScenes, query ? 250 : 0);
    return () => clearTimeout(t);
  }, [loadScenes, query]);

  useEffect(() => {
    if (!menuFor) return;
    const close = () => setMenuFor(null);
    window.addEventListener("click", close);
    return () => window.removeEventListener("click", close);
  }, [menuFor]);

  const refresh = () => {
    loadScenes();
    loadCollections();
  };

  const newScene = async () => {
    try {
      const { id } = await api.createScene({
        title: "Sem título",
        collectionId: view.kind === "collection" ? view.id : null,
      });
      navigate(`/scene/${id}`);
    } catch (e) {
      fail(e);
    }
  };

  const newCollection = async () => {
    const name = prompt("Nome da coleção");
    if (!name?.trim()) return;
    try {
      const c = await api.createCollection(name);
      await loadCollections();
      setView({ kind: "collection", id: c.id });
    } catch (e) {
      fail(e);
    }
  };

  // Importing .excalidraw files is the migration path out of the hosted product.
  const importFiles = async (files: FileList | null) => {
    if (!files?.length) return;
    let ok = 0;
    const failed: string[] = [];
    for (const file of Array.from(files)) {
      try {
        const data = JSON.parse(await file.text());
        if (!Array.isArray(data.elements)) throw new Error("not a scene");
        const { id } = await api.createScene({
          title: file.name.replace(/\.(excalidraw|json)$/i, ""),
          collectionId: view.kind === "collection" ? view.id : null,
          elements: data.elements,
          appState: pickAppState(data.appState),
        });
        await uploadEmbeddedFiles(id, data.files);
        ok++;
      } catch {
        failed.push(file.name);
      }
    }
    if (fileInput.current) fileInput.current.value = "";
    setError(failed.length ? `Não foi possível importar: ${failed.join(", ")}` : "");
    if (ok) refresh();
  };

  const act = async (fn: () => Promise<unknown>) => {
    try {
      await fn();
      refresh();
    } catch (e) {
      fail(e);
    }
  };

  const title =
    view.kind === "all"
      ? "Todos os desenhos"
      : view.kind === "none"
        ? "Sem coleção"
        : view.kind === "trash"
          ? "Lixeira"
          : view.kind === "members"
            ? "Membros"
            : (collections.find((c) => c.id === view.id)?.name ?? "Coleção");

  const navItem = (v: View, label: string, count?: number) => (
    <button
      className={`nav-item${viewKey(v) === viewKey(view) ? " active" : ""}`}
      onClick={() => {
        setView(v);
        setScenes(null);
        setNavOpen(false);
      }}
    >
      <span className="ellipsis">{label}</span>
      {count !== undefined && <span className="count">{count}</span>}
    </button>
  );

  return (
    <div className="dash">
      <aside className={`dash-nav${navOpen ? " open" : ""}`}>
        <div className="brand">
          <img src="/favicon.svg" alt="" width={26} height={26} />
          <strong>oute draw</strong>
        </div>
        {navItem({ kind: "all" }, "Todos os desenhos")}
        {navItem({ kind: "none" }, "Sem coleção")}
        <div className="nav-heading">
          <span>Coleções</span>
          {canEdit && (
            <button className="icon-btn" title="Nova coleção" onClick={newCollection}>
              +
            </button>
          )}
        </div>
        {collections.map((c) => (
          <div key={c.id}>{navItem({ kind: "collection", id: c.id }, c.name, c.sceneCount)}</div>
        ))}
        {collections.length === 0 && <p className="muted small pad">Nenhuma coleção ainda.</p>}
        <div className="nav-spacer" />
        {navItem({ kind: "trash" }, "Lixeira")}
        {navItem({ kind: "members" }, "Membros")}
        {me && (
          <div className="whoami">
            <div className="ellipsis">{me.user.name}</div>
            <div className="muted small ellipsis">
              {me.user.email} · {roleLabel[me.user.role]}
            </div>
          </div>
        )}
      </aside>

      <main className="dash-main">
        <header className="dash-head">
          <button className="icon-btn nav-toggle" aria-label="Menu" onClick={() => setNavOpen((o) => !o)}>
            ☰
          </button>
          <h1 className="ellipsis">{title}</h1>
          {view.kind === "collection" && canEdit && (
            <>
              <button
                className="btn ghost"
                onClick={() => {
                  const name = prompt("Novo nome da coleção", title);
                  if (name?.trim()) act(() => api.renameCollection(view.id, name));
                }}
              >
                Renomear
              </button>
              <button
                className="btn ghost"
                onClick={() => {
                  if (confirm(`Excluir a coleção "${title}"? Os desenhos dela ficam em "Sem coleção".`)) {
                    act(() => api.deleteCollection(view.id)).then(() => setView({ kind: "all" }));
                  }
                }}
              >
                Excluir
              </button>
            </>
          )}
          <div className="grow" />
          {view.kind !== "members" && (
            <input
              className="search"
              type="search"
              placeholder="Buscar por título ou texto do desenho"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          )}
          {canEdit && view.kind !== "trash" && view.kind !== "members" && (
            <>
              <input
                ref={fileInput}
                type="file"
                accept=".excalidraw,.json,application/json"
                multiple
                hidden
                onChange={(e) => importFiles(e.target.files)}
              />
              <button className="btn" onClick={() => fileInput.current?.click()}>
                Importar
              </button>
              <button className="btn primary" onClick={newScene}>
                Novo desenho
              </button>
            </>
          )}
        </header>

        {error && (
          <div className="banner error" role="alert">
            {error}
            <button className="icon-btn" onClick={() => setError("")} aria-label="Fechar">
              ×
            </button>
          </div>
        )}

        {view.kind === "members" ? (
          <Members me={me} onError={fail} />
        ) : scenes === null ? (
          <p className="muted pad">Carregando…</p>
        ) : scenes.length === 0 ? (
          <div className="empty">
            {query ? (
              <p>Nenhum desenho encontrado para “{query}”.</p>
            ) : view.kind === "trash" ? (
              <p>A lixeira está vazia.</p>
            ) : (
              <>
                <p>Nenhum desenho aqui ainda.</p>
                {canEdit && (
                  <p className="muted">
                    Crie um novo ou importe seus arquivos <code>.excalidraw</code>.
                  </p>
                )}
              </>
            )}
          </div>
        ) : (
          <div className="grid">
            {scenes.map((s) => (
              <article key={s.id} className="card">
                <a
                  className="thumb"
                  href={`/scene/${s.id}`}
                  onClick={(e) => {
                    if (e.metaKey || e.ctrlKey || e.shiftKey) return;
                    e.preventDefault();
                    navigate(`/scene/${s.id}`);
                  }}
                >
                  {s.hasThumbnail ? (
                    <img src={`/api/scenes/${s.id}/thumbnail?v=${s.version}`} alt="" loading="lazy" />
                  ) : (
                    <span className="muted small">Sem prévia</span>
                  )}
                </a>
                <div className="card-foot">
                  <div className="card-text">
                    <div className="ellipsis card-title" title={s.title}>
                      {s.title}
                    </div>
                    <div className="muted small ellipsis">
                      {timeAgo(s.updatedAt)}
                      {s.ownerName && ` · ${s.ownerName}`}
                    </div>
                  </div>
                  {canEdit && (
                    <div className="menu-wrap">
                      <button
                        className="icon-btn"
                        aria-label={`Ações de ${s.title}`}
                        onClick={(e) => {
                          e.stopPropagation();
                          setMenuFor(menuFor === s.id ? null : s.id);
                        }}
                      >
                        ⋯
                      </button>
                      {menuFor === s.id && (
                        <div className="menu">
                          {view.kind === "trash" ? (
                            <>
                              <button onClick={() => act(() => api.restoreScene(s.id))}>Restaurar</button>
                              <button
                                className="danger"
                                onClick={() => {
                                  if (confirm(`Excluir "${s.title}" para sempre? Isso não pode ser desfeito.`)) {
                                    act(() => api.purgeScene(s.id));
                                  }
                                }}
                              >
                                Excluir para sempre
                              </button>
                            </>
                          ) : (
                            <>
                              <button
                                onClick={() => {
                                  const t = prompt("Título", s.title);
                                  if (t?.trim()) act(() => api.patchScene(s.id, { title: t }));
                                }}
                              >
                                Renomear
                              </button>
                              <button onClick={() => act(() => api.duplicateScene(s.id, `${s.title} (cópia)`))}>
                                Duplicar
                              </button>
                              <div className="menu-label">Mover para</div>
                              {s.collectionId && (
                                <button onClick={() => act(() => api.patchScene(s.id, { collectionId: null }))}>
                                  Sem coleção
                                </button>
                              )}
                              {collections
                                .filter((c) => c.id !== s.collectionId)
                                .map((c) => (
                                  <button key={c.id} onClick={() => act(() => api.patchScene(s.id, { collectionId: c.id }))}>
                                    {c.name}
                                  </button>
                                ))}
                              <hr />
                              <button className="danger" onClick={() => act(() => api.trashScene(s.id))}>
                                Mover para a lixeira
                              </button>
                            </>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
              </article>
            ))}
          </div>
        )}
      </main>
    </div>
  );
};

const roleLabel: Record<Role, string> = { admin: "Admin", editor: "Editor", viewer: "Leitor" };

const Members = ({ me, onError }: { me: Me | null; onError: (e: unknown) => void }) => {
  const [users, setUsers] = useState<User[] | null>(null);
  const load = useCallback(() => api.users().then(setUsers).catch(onError), [onError]);
  useEffect(() => {
    load();
  }, [load]);
  if (!users) return <p className="muted pad">Carregando…</p>;
  const isAdmin = me?.user.role === "admin";
  return (
    <div className="pad">
      <p className="muted">
        Quem pode entrar é decidido no login (lista de e-mails do oauth2-proxy). Aqui você define o que cada pessoa pode
        fazer depois de entrar.
      </p>
      <table className="table">
        <thead>
          <tr>
            <th>Nome</th>
            <th>E-mail</th>
            <th>Papel</th>
            <th>Visto</th>
          </tr>
        </thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id}>
              <td>{u.name}</td>
              <td>{u.email}</td>
              <td>
                {isAdmin && u.id !== me?.user.id ? (
                  <select
                    value={u.role}
                    aria-label={`Papel de ${u.email}`}
                    onChange={(e) =>
                      api
                        .setRole(u.id, e.target.value as Role)
                        .then(load)
                        .catch(onError)
                    }
                  >
                    <option value="admin">Admin</option>
                    <option value="editor">Editor</option>
                    <option value="viewer">Leitor</option>
                  </select>
                ) : (
                  roleLabel[u.role]
                )}
              </td>
              <td className="muted">{timeAgo(u.lastSeenAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

/** Only the look of the canvas travels with a scene; view position and tools do not. */
export const pickAppState = (state: object | undefined) => {
  const s = state as Record<string, unknown> | undefined;
  const out: Record<string, unknown> = {};
  for (const k of ["viewBackgroundColor", "gridSize", "gridStep", "gridModeEnabled"]) {
    if (s && s[k] !== undefined) out[k] = s[k];
  }
  return out;
};

const uploadEmbeddedFiles = async (sceneId: string, files: Record<string, { dataURL?: string }> | undefined) => {
  for (const [fileId, f] of Object.entries(files ?? {})) {
    if (!f?.dataURL) continue;
    const blob = await (await fetch(f.dataURL)).blob();
    const res = await fetch(`/api/scenes/${sceneId}/files/${fileId}`, {
      method: "PUT",
      headers: { "Content-Type": blob.type },
      body: blob,
    });
    if (!res.ok) throw new Error("upload failed");
  }
};
