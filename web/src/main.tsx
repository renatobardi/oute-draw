import { StrictMode, Suspense, lazy, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";

import "./styles.css";
import { Dashboard } from "./pages/Dashboard";

// The editor is by far the largest chunk; the dashboard must not wait for it.
const ScenePage = lazy(() => import("./pages/ScenePage"));
const SharePage = lazy(() => import("./pages/SharePage"));

const usePath = () => {
  const [path, setPath] = useState(location.pathname);
  useEffect(() => {
    const on = () => setPath(location.pathname);
    window.addEventListener("popstate", on);
    return () => window.removeEventListener("popstate", on);
  }, []);
  return path;
};

const App = () => {
  const path = usePath();
  const scene = path.match(/^\/scene\/([^/]+)$/);
  const share = path.match(/^\/s\/([^/]+)$/);
  return (
    <Suspense fallback={<div className="center-screen muted">Carregando…</div>}>
      {scene ? (
        <ScenePage key={scene[1]} sceneId={scene[1]} />
      ) : share ? (
        <SharePage key={share[1]} token={share[1]} />
      ) : (
        <Dashboard />
      )}
    </Suspense>
  );
};

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
