// Typed access to the Go API. Everything the UI knows about the server is here.

export type Role = "admin" | "editor" | "viewer";

export interface User {
  id: string;
  email: string;
  name: string;
  role: Role;
  lastSeenAt: string;
}

export interface Me {
  user: User;
  features: { ai: boolean };
}

export interface Collection {
  id: string;
  name: string;
  sceneCount: number;
}

export interface SceneMeta {
  id: string;
  title: string;
  collectionId: string | null;
  ownerName: string;
  version: number;
  hasThumbnail: boolean;
  createdAt: string;
  updatedAt: string;
  deletedAt: string | null;
}

export interface SceneData extends SceneMeta {
  elements: unknown[];
  appState: Record<string, unknown>;
  slideOrder: string[];
  fileIds: string[];
}

export interface Access {
  canEdit: boolean;
  canComment: boolean;
  guest: boolean;
  name: string;
}

export interface SceneResponse {
  scene: SceneData;
  access: Access;
  iceServers: RTCIceServer[];
}

export interface Version {
  id: string;
  label: string;
  auto: boolean;
  authorName: string;
  createdAt: string;
}

export interface VersionContent extends Version {
  elements: unknown[];
  appState: Record<string, unknown>;
}

export interface ShareLink {
  id: string;
  token: string;
  mode: "view" | "edit";
  hasPassword: boolean;
  expiresAt: string | null;
  createdAt: string;
}

export interface Comment {
  id: string;
  threadId: string | null;
  authorId: string | null;
  authorName: string;
  x: number;
  y: number;
  body: string;
  resolvedAt: string | null;
  createdAt: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

const messages: Record<number, string> = {
  401: "Sessão expirada. Recarregue a página para entrar de novo.",
  403: "Você não tem permissão para isso.",
  404: "Não encontrado.",
  413: "Grande demais para enviar.",
};

export async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  init: { headers?: Record<string, string>; keepalive?: boolean; raw?: BodyInit } = {},
): Promise<T> {
  // Asking for JSON makes the login proxy answer 401 instead of redirecting a
  // background request to the identity provider.
  const headers: Record<string, string> = { Accept: "application/json", ...init.headers };
  let payload: BodyInit | undefined = init.raw;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: payload, keepalive: init.keepalive });
  } catch {
    throw new ApiError(0, "Sem conexão com o servidor.");
  }
  if (!res.ok) {
    let detail = "";
    try {
      detail = (await res.json()).error ?? "";
    } catch {
      /* not JSON: the proxy answered, not the app */
    }
    throw new ApiError(res.status, messages[res.status] ?? (detail || `Erro ${res.status}`));
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

const get = <T>(path: string) => request<T>("GET", path);

export const api = {
  me: () => get<Me>("/api/me"),
  users: () => get<User[]>("/api/users"),
  setRole: (id: string, role: Role) => request<void>("PATCH", `/api/users/${id}`, { role }),

  library: () => get<unknown[]>("/api/library"),
  saveLibrary: (items: unknown) => request<void>("PUT", "/api/library", items),

  collections: () => get<Collection[]>("/api/collections"),
  createCollection: (name: string) => request<Collection>("POST", "/api/collections", { name }),
  renameCollection: (id: string, name: string) => request<void>("PATCH", `/api/collections/${id}`, { name }),
  deleteCollection: (id: string) => request<void>("DELETE", `/api/collections/${id}`),

  scenes: (f: { collection?: string; q?: string; trash?: boolean }) => {
    const p = new URLSearchParams();
    if (f.collection) p.set("collection", f.collection);
    if (f.q) p.set("q", f.q);
    if (f.trash) p.set("trash", "1");
    return get<SceneMeta[]>(`/api/scenes?${p}`);
  },
  createScene: (s: { title: string; collectionId?: string | null; elements?: unknown[]; appState?: unknown }) =>
    request<{ id: string }>("POST", "/api/scenes", s),
  patchScene: (id: string, patch: { title?: string; collectionId?: string | null; slideOrder?: string[] }) =>
    request<void>("PATCH", `/api/scenes/${id}`, patch),
  trashScene: (id: string) => request<void>("DELETE", `/api/scenes/${id}`),
  purgeScene: (id: string) => request<void>("DELETE", `/api/scenes/${id}?permanent=1`),
  restoreScene: (id: string) => request<void>("POST", `/api/scenes/${id}/restore`),
  duplicateScene: (id: string, title: string) =>
    request<{ id: string }>("POST", `/api/scenes/${id}/duplicate`, { title }),

  versions: (id: string) => get<Version[]>(`/api/scenes/${id}/versions`),
  version: (id: string, vid: string) => get<VersionContent>(`/api/scenes/${id}/versions/${vid}`),
  createVersion: (id: string, label: string) => request<Version>("POST", `/api/scenes/${id}/versions`, { label }),
  deleteVersion: (id: string, vid: string) => request<void>("DELETE", `/api/scenes/${id}/versions/${vid}`),
  restoreVersion: (id: string, vid: string) => request<void>("POST", `/api/scenes/${id}/versions/${vid}/restore`),

  shares: (id: string) => get<ShareLink[]>(`/api/scenes/${id}/shares`),
  createShare: (id: string, s: { mode: "view" | "edit"; password?: string; expiresAt?: string | null }) =>
    request<ShareLink>("POST", `/api/scenes/${id}/shares`, s),
  deleteShare: (id: string, sid: string) => request<void>("DELETE", `/api/scenes/${id}/shares/${sid}`),

  textToDiagram: (prompt: string) => request<{ mermaid: string }>("POST", "/api/ai/text-to-diagram", { prompt }),

  shareInfo: (token: string) => get<{ mode: "view" | "edit"; hasPassword: boolean }>(`/api/public/s/${token}/info`),
  shareUnlock: (token: string, password: string) =>
    request<{ access: string }>("POST", `/api/public/s/${token}/unlock`, { password }),
};

/**
 * A door is one way into a scene's editing surface: a member by scene id, or a
 * guest by share token. The editor only ever talks through a door, so both cases
 * run the same code.
 */
export interface Door {
  base: string;
  headers: Record<string, string>;
  wsQuery: string;
  /** Scene id for member-only features (history, sharing); null for guests. */
  memberSceneId: string | null;
}

export const memberDoor = (sceneId: string): Door => ({
  base: `/api/scenes/${sceneId}`,
  headers: {},
  wsQuery: "",
  memberSceneId: sceneId,
});

export const linkDoor = (token: string, guestName: string, access: string): Door => {
  const q = new URLSearchParams({ name: guestName });
  const headers: Record<string, string> = { "X-Guest-Name": encodeURIComponent(guestName) };
  if (access) {
    q.set("access", access);
    headers["X-Share-Access"] = access;
  }
  return { base: `/api/public/s/${token}`, headers, wsQuery: q.toString(), memberSceneId: null };
};

export const sceneApi = (door: Door) => {
  const h = { headers: door.headers };
  return {
    load: () => request<SceneResponse>("GET", door.base, undefined, h),
    save: (body: { elements: unknown[]; appState?: unknown; thumbnail?: string }, keepalive = false) =>
      request<{ version: number; changed: boolean }>("PUT", `${door.base}/content`, body, { ...h, keepalive }),
    getFile: async (fileId: string): Promise<Blob> => {
      const res = await fetch(`${door.base}/files/${fileId}`, { headers: door.headers });
      if (!res.ok) throw new ApiError(res.status, "Imagem indisponível.");
      return res.blob();
    },
    putFile: (fileId: string, blob: Blob) =>
      request<void>("PUT", `${door.base}/files/${fileId}`, undefined, {
        headers: { ...door.headers, "Content-Type": blob.type },
        raw: blob,
      }),
    comments: () => request<Comment[]>("GET", `${door.base}/comments`, undefined, h),
    addComment: (c: { body: string; x?: number; y?: number; threadId?: string }) =>
      request<Comment>("POST", `${door.base}/comments`, c, h),
    patchComment: (id: string, p: { body?: string; resolved?: boolean }) =>
      request<void>("PATCH", `${door.base}/comments/${id}`, p, h),
    deleteComment: (id: string) => request<void>("DELETE", `${door.base}/comments/${id}`, undefined, h),
    wsUrl: () => {
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      return `${proto}//${location.host}${door.base}/ws${door.wsQuery ? `?${door.wsQuery}` : ""}`;
    },
  };
};

export type SceneApi = ReturnType<typeof sceneApi>;

export const timeAgo = (iso: string): string => {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return "agora";
  if (s < 3600) return `há ${Math.floor(s / 60)} min`;
  if (s < 86400) return `há ${Math.floor(s / 3600)} h`;
  if (s < 86400 * 30) return `há ${Math.floor(s / 86400)} d`;
  return new Date(iso).toLocaleDateString("pt-BR");
};
