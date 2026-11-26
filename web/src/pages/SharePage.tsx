import { useEffect, useMemo, useState } from "react";

import { ApiError, api, linkDoor } from "../api";
import { Editor } from "../editor/Editor";

const NAME_KEY = "oute-draw:guest-name";
const read = (store: Storage, key: string) => {
  try {
    return store.getItem(key) ?? "";
  } catch {
    return ""; // storage can be blocked inside an iframe
  }
};
const write = (store: Storage, key: string, value: string) => {
  try {
    store.setItem(key, value);
  } catch {
    /* same */
  }
};

/** A scene opened through a share link, by someone without an account. */
const SharePage = ({ token }: { token: string }) => {
  const params = new URLSearchParams(location.search);
  const embed = params.get("embed") === "1";
  const present = params.get("present") === "1";
  const accessKey = `oute-draw:access:${token}`;

  const [info, setInfo] = useState<{ mode: "view" | "edit"; hasPassword: boolean } | null>(null);
  const [error, setError] = useState("");
  const [access, setAccess] = useState(() => read(sessionStorage, accessKey));
  const [name, setName] = useState(() => read(localStorage, NAME_KEY));
  const [nameDraft, setNameDraft] = useState("");
  const [password, setPassword] = useState("");
  const [wrong, setWrong] = useState(false);

  useEffect(() => {
    api
      .shareInfo(token)
      .then(setInfo)
      .catch((e) =>
        setError(e instanceof ApiError && e.status === 404 ? "Este link não existe mais ou expirou." : "Não foi possível abrir o link."),
      );
  }, [token]);

  // Only people who can change the drawing need a name; readers stay anonymous.
  const needsName = info?.mode === "edit" && !name;
  const guestName = name || "Convidado";
  const door = useMemo(() => linkDoor(token, guestName, access), [token, guestName, access]);

  if (error) return <div className="center-screen">{error}</div>;
  if (!info) return <div className="center-screen muted">Carregando…</div>;

  if (info.hasPassword && !access) {
    return (
      <form
        className="center-screen gate"
        onSubmit={async (e) => {
          e.preventDefault();
          try {
            const res = await api.shareUnlock(token, password);
            write(sessionStorage, accessKey, res.access);
            setAccess(res.access);
          } catch {
            setWrong(true);
          }
        }}
      >
        <h1>Desenho protegido</h1>
        <p className="muted">Digite a senha que veio com o link.</p>
        <input
          type="password"
          autoFocus
          value={password}
          aria-label="Senha"
          onChange={(e) => {
            setPassword(e.target.value);
            setWrong(false);
          }}
        />
        {wrong && <p className="error-text">Senha incorreta.</p>}
        <button className="btn primary" disabled={!password}>
          Abrir
        </button>
      </form>
    );
  }

  if (needsName) {
    return (
      <form
        className="center-screen gate"
        onSubmit={(e) => {
          e.preventDefault();
          const n = nameDraft.trim().slice(0, 60);
          if (!n) return;
          write(localStorage, NAME_KEY, n);
          setName(n);
        }}
      >
        <h1>Como devemos te chamar?</h1>
        <p className="muted">Seu nome aparece para quem estiver editando junto.</p>
        <input autoFocus value={nameDraft} aria-label="Seu nome" maxLength={60} onChange={(e) => setNameDraft(e.target.value)} />
        <button className="btn primary" disabled={!nameDraft.trim()}>
          Entrar
        </button>
      </form>
    );
  }

  return <Editor door={door} me={null} embed={embed} present={present} />;
};

export default SharePage;
