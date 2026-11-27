import { useEffect, useMemo, useState } from "react";

import { api, memberDoor, type Me } from "../api";
import { Editor } from "../editor/Editor";

/** A scene opened by a signed-in member of the workspace. */
const ScenePage = ({ sceneId }: { sceneId: string }) => {
  const [me, setMe] = useState<Me | null>(null);
  const [failed, setFailed] = useState(false);
  const door = useMemo(() => memberDoor(sceneId), [sceneId]);
  useEffect(() => {
    api
      .me()
      .then(setMe)
      .catch(() => setFailed(true));
  }, []);
  if (failed) {
    return <div className="center-screen">Sessão expirada. Recarregue a página para entrar de novo.</div>;
  }
  if (!me) return <div className="center-screen muted">Carregando…</div>;
  return <Editor door={door} me={me} />;
};

export default ScenePage;
