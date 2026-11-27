import {
  Excalidraw,
  MainMenu,
  TTDDialog,
  TTDDialogTrigger,
  WelcomeScreen,
  restoreElements,
  useHandleLibrary,
} from "@excalidraw/excalidraw";
import "@excalidraw/excalidraw/index.css";
import type { ExcalidrawElement, OrderedExcalidrawElement } from "@excalidraw/excalidraw/element/types";
import type {
  AppState,
  BinaryFiles,
  Collaborator,
  ExcalidrawImperativeAPI,
  ExcalidrawInitialDataState,
  LibraryItems,
  SocketId,
} from "@excalidraw/excalidraw/types";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { ApiError, api, sceneApi as makeSceneApi, type Comment, type Door, type Me, type SceneResponse } from "../api";
import { navigate } from "../nav";
import { pickAppState } from "../pages/Dashboard";
import { Collab, type Peer, type ServerEvent } from "./collab";
import { CommentLayer, CommentsPanel, threadsOf, type Viewport } from "./Comments";
import { HistoryPanel } from "./History";
import { PresentBar, SlidesPanel, slideName, slidesOf } from "./Presentation";
import { ShareDialog } from "./ShareDialog";
import { SceneSync, type SaveState } from "./sync";
import { Call, type CallState } from "./voice";

interface Props {
  door: Door;
  /** The signed-in member, or null when the scene was opened through a share link. */
  me: Me | null;
  /** Start in presentation mode (share links with ?present=1). */
  present?: boolean;
  /** Bare canvas for iframes (share links with ?embed=1). */
  embed?: boolean;
}

type Panel = "comments" | "history" | "slides" | null;

const saveLabel: Record<SaveState, string> = {
  saved: "Salvo",
  dirty: "Alterações não salvas",
  saving: "Salvando…",
  error: "Falha ao salvar. Tentando de novo…",
};

const sameViewport = (a: Viewport, b: Viewport) =>
  a.scrollX === b.scrollX &&
  a.scrollY === b.scrollY &&
  a.zoom.value === b.zoom.value &&
  a.offsetLeft === b.offsetLeft &&
  a.offsetTop === b.offsetTop;

export const Editor = ({ door, me, present, embed }: Props) => {
  const sceneApi = useMemo(() => makeSceneApi(door), [door]);
  const [loaded, setLoaded] = useState<SceneResponse | null>(null);
  const [library, setLibrary] = useState<unknown[]>([]);
  const [fatal, setFatal] = useState("");
  const [excalidraw, setExcalidraw] = useState<ExcalidrawImperativeAPI | null>(null);

  const [title, setTitle] = useState("");
  const [slideOrder, setSlideOrder] = useState<string[]>([]);
  const [saveState, setSaveState] = useState<SaveState>("saved");
  const [online, setOnline] = useState(false);
  const [peers, setPeers] = useState<Peer[]>([]);
  const [panel, setPanel] = useState<Panel>(null);
  const [shareOpen, setShareOpen] = useState(false);
  const [comments, setComments] = useState<Comment[]>([]);
  const [placing, setPlacing] = useState(false);
  const [openThread, setOpenThread] = useState<string | null>(null);
  const [viewport, setViewport] = useState<Viewport>({ scrollX: 0, scrollY: 0, zoom: { value: 1 }, offsetLeft: 0, offsetTop: 0 });
  const [frameKey, setFrameKey] = useState("");
  const [presenting, setPresenting] = useState<number | null>(null);
  const [laser, setLaser] = useState(false);
  const [call, setCall] = useState<CallState>({ inCall: false, muted: false, sharing: false, peers: new Map(), error: "" });

  const syncRef = useRef<SceneSync | null>(null);
  const collabRef = useRef<Collab | null>(null);
  const callRef = useRef<Call | null>(null);
  const collaborators = useRef(new Map<SocketId, Collaborator>());
  const rootRef = useRef<HTMLDivElement>(null);
  const viewportRef = useRef(viewport);
  const frameKeyRef = useRef("");
  const lastPointer = useRef(0);
  const libraryTimer = useRef<ReturnType<typeof setTimeout>>();

  const access = loaded?.access;
  const canEdit = !!access?.canEdit;
  const isMember = door.memberSceneId !== null;
  const sceneId = door.memberSceneId;

  const notice = useCallback(
    (message: string) => excalidraw?.setToast({ message, closable: true, duration: 6000 }),
    [excalidraw],
  );
  const noticeRef = useRef(notice);
  noticeRef.current = notice;

  // ------------------------------------------------------------ load

  useEffect(() => {
    let live = true;
    Promise.all([sceneApi.load(), isMember ? api.library().catch(() => []) : Promise.resolve([])])
      .then(([res, lib]) => {
        if (!live) return;
        setLibrary(lib);
        setTitle(res.scene.title);
        setSlideOrder(res.scene.slideOrder ?? []);
        setLoaded(res);
      })
      .catch((e) => live && setFatal(e instanceof ApiError ? e.message : "Não foi possível abrir o desenho."));
    return () => {
      live = false;
    };
  }, [sceneApi, isMember]);

  useEffect(() => {
    document.title = title ? `${title} · oute draw` : "oute draw";
  }, [title]);

  const initialData = useMemo<ExcalidrawInitialDataState | null>(() => {
    if (!loaded) return null;
    return {
      elements: restoreElements(loaded.scene.elements as ExcalidrawElement[], null),
      appState: pickAppState(loaded.scene.appState) as Partial<AppState>,
      libraryItems: library as LibraryItems,
      scrollToContent: true,
    };
  }, [loaded, library]);

  const loadComments = useCallback(() => {
    sceneApi
      .comments()
      .then(setComments)
      .catch(() => undefined);
  }, [sceneApi]);

  // ------------------------------------------------------------ sync + collaboration

  useEffect(() => {
    if (!excalidraw || !loaded) return;
    const sync = new SceneSync(
      excalidraw,
      sceneApi,
      loaded.access.canEdit,
      () => collabRef.current,
      setSaveState,
      (m) => noticeRef.current(m),
    );
    syncRef.current = sync;
    sync.seed(excalidraw.getSceneElementsIncludingDeleted(), loaded.scene.appState, loaded.scene.fileIds);
    loadComments();

    const voice = new Call(() => collabRef.current, loaded.iceServers ?? [], (s) => {
      setCall(s);
      for (const [id, c] of collaborators.current) {
        const p = s.peers.get(id);
        collaborators.current.set(id, { ...c, isInCall: !!p, isMuted: !!p?.muted });
      }
      excalidraw.updateScene({ collaborators: new Map(collaborators.current) });
    });
    callRef.current = voice;

    const refreshMeta = () =>
      sync
        .resync()
        .then((scene) => {
          setTitle(scene.title);
          setSlideOrder(scene.slideOrder ?? []);
        })
        .catch(() => undefined);

    let greeted = false;
    const onEvent = (e: ServerEvent) => {
      switch (e.t) {
        case "hello": {
          collaborators.current = new Map(
            e.peers.map((p) => [p.id as SocketId, { id: p.id, socketId: p.id as SocketId, username: p.name }]),
          );
          setPeers(e.peers);
          // After a reconnect, whatever happened meanwhile is in the stored scene.
          if (greeted) {
            void refreshMeta();
            loadComments();
          }
          greeted = true;
          break;
        }
        case "join":
          collaborators.current.set(e.peer.id as SocketId, {
            id: e.peer.id,
            socketId: e.peer.id as SocketId,
            username: e.peer.name,
          });
          setPeers((ps) => [...ps.filter((p) => p.id !== e.peer.id), e.peer]);
          sync.relayAll();
          voice.peerJoined(e.peer.id);
          break;
        case "leave":
          collaborators.current.delete(e.id as SocketId);
          setPeers((ps) => ps.filter((p) => p.id !== e.id));
          voice.peerLeft(e.id);
          break;
        case "relay": {
          const m = e.m;
          if (m.t === "update" && Array.isArray(m.elements)) {
            sync.applyRemote(m.elements);
            return;
          }
          if (m.t === "pointer") {
            const id = e.from as SocketId;
            const known = collaborators.current.get(id);
            if (!known) return;
            collaborators.current.set(id, {
              ...known,
              pointer: m.pointer as Collaborator["pointer"],
              button: m.button as Collaborator["button"],
              selectedElementIds: m.selectedElementIds as Collaborator["selectedElementIds"],
            });
            break;
          }
          if (m.t === "call" || m.t === "rtc") {
            void voice.handle(e.from, m as Parameters<Call["handle"]>[1]);
          }
          return;
        }
        case "comments":
          loadComments();
          return;
        case "reload":
        case "meta":
          void refreshMeta();
          return;
      }
      excalidraw.updateScene({ collaborators: new Map(collaborators.current) });
    };
    const collab = new Collab(sceneApi.wsUrl, onEvent, setOnline);
    collabRef.current = collab;

    const onUnload = () => void sync.flush({ keepalive: true });
    window.addEventListener("pagehide", onUnload);
    return () => {
      window.removeEventListener("pagehide", onUnload);
      voice.dispose();
      collab.close();
      collabRef.current = null;
      callRef.current = null;
      // Leaving the scene: save what is pending, with a fresh preview for the dashboard.
      void sync.flush({ forceThumbnail: loaded.access.canEdit }).finally(() => sync.dispose());
      syncRef.current = null;
    };
  }, [excalidraw, loaded, sceneApi, loadComments]);

  useHandleLibrary({ excalidrawAPI: excalidraw });

  const onChange = useCallback((elements: readonly OrderedExcalidrawElement[], appState: AppState, files: BinaryFiles) => {
    syncRef.current?.onChange(elements, appState, files);
    if (!sameViewport(viewportRef.current, appState)) {
      viewportRef.current = {
        scrollX: appState.scrollX,
        scrollY: appState.scrollY,
        zoom: appState.zoom,
        offsetLeft: appState.offsetLeft,
        offsetTop: appState.offsetTop,
      };
      setViewport(viewportRef.current);
    }
    // Slides depend on frames only: track them with a cheap key instead of state per change.
    let key = "";
    for (const el of elements) {
      if (el.type === "frame" && !el.isDeleted) key += `${el.id}:${el.version},`;
    }
    if (key !== frameKeyRef.current) {
      frameKeyRef.current = key;
      setFrameKey(key);
    }
  }, []);

  const onPointerUpdate = useCallback(
    (payload: { pointer: { x: number; y: number; tool: "pointer" | "laser" }; button: "down" | "up" }) => {
      const now = performance.now();
      const collab = collabRef.current;
      if (!collab?.online || now - lastPointer.current < 33 || collaborators.current.size === 0) return;
      lastPointer.current = now;
      collab.send({
        t: "pointer",
        pointer: payload.pointer,
        button: payload.button,
        selectedElementIds: excalidraw?.getAppState().selectedElementIds,
      });
    },
    [excalidraw],
  );

  const onLibraryChange = useCallback(
    (items: LibraryItems) => {
      if (!isMember) return;
      clearTimeout(libraryTimer.current);
      libraryTimer.current = setTimeout(() => void api.saveLibrary(items).catch(() => undefined), 1000);
    },
    [isMember],
  );

  // ------------------------------------------------------------ title

  const commitTitle = async () => {
    const next = title.trim() || "Sem título";
    setTitle(next);
    if (!sceneId || next === loaded?.scene.title) return;
    try {
      await api.patchScene(sceneId, { title: next });
      if (loaded) loaded.scene.title = next;
    } catch (e) {
      notice(e instanceof ApiError ? e.message : "Não foi possível renomear.");
    }
  };

  // ------------------------------------------------------------ presentation

  const slides = useMemo(
    () => (excalidraw ? slidesOf(excalidraw.getSceneElements(), slideOrder) : []),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [excalidraw, slideOrder, frameKey],
  );

  const source = useCallback(
    () => ({
      elements: excalidraw!.getSceneElements(),
      appState: excalidraw!.getAppState(),
      files: excalidraw!.getFiles(),
    }),
    [excalidraw],
  );

  const startPresenting = useCallback(
    (index: number) => {
      if (!slides.length) {
        notice("Crie frames (tecla F) para definir os slides da apresentação.");
        setPanel("slides");
        return;
      }
      setPanel(null);
      setPlacing(false);
      setOpenThread(null);
      setPresenting(Math.min(index, slides.length - 1));
      void rootRef.current?.requestFullscreen?.().catch(() => undefined);
    },
    [slides.length, notice],
  );

  const stopPresenting = useCallback(() => {
    setPresenting(null);
    setLaser(false);
    if (document.fullscreenElement) void document.exitFullscreen().catch(() => undefined);
  }, []);

  const fitSlide = useCallback(
    (animate: boolean) => {
      if (presenting === null || !excalidraw) return;
      const frame = slides[presenting];
      if (frame) excalidraw.scrollToContent(frame, { fitToViewport: true, viewportZoomFactor: 1, animate, duration: 350 });
    },
    [presenting, excalidraw, slides],
  );

  useEffect(() => {
    if (presenting === null || !excalidraw) return;
    excalidraw.updateFrameRendering({ enabled: true, name: false, outline: false, clip: true });
    return () => {
      excalidraw.updateFrameRendering({ enabled: true, name: true, outline: true, clip: true });
      excalidraw.setActiveTool({ type: "selection" });
    };
  }, [presenting === null, excalidraw]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (presenting === null) return;
    // The canvas resizes when entering fullscreen; fit once now and again after it.
    fitSlide(true);
    const refit = () => setTimeout(() => fitSlide(false), 60);
    const onFullscreen = () => {
      if (!document.fullscreenElement) setPresenting(null);
      else refit();
    };
    const onKey = (e: KeyboardEvent) => {
      const next = ["ArrowRight", "ArrowDown", "PageDown", " ", "Enter"].includes(e.key);
      const prev = ["ArrowLeft", "ArrowUp", "PageUp", "Backspace"].includes(e.key);
      if (!next && !prev && e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      if (e.key === "Escape") stopPresenting();
      else setPresenting((i) => (i === null ? i : Math.max(0, Math.min(slides.length - 1, i + (next ? 1 : -1)))));
    };
    window.addEventListener("resize", refit);
    document.addEventListener("fullscreenchange", onFullscreen);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("resize", refit);
      document.removeEventListener("fullscreenchange", onFullscreen);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [presenting, fitSlide, slides.length, stopPresenting]);

  // A presentation link opens straight into the first slide.
  const autoPresented = useRef(false);
  useEffect(() => {
    if (present && excalidraw && slides.length && !autoPresented.current) {
      autoPresented.current = true;
      setPresenting(0);
    }
  }, [present, excalidraw, slides.length]);

  const reorderSlides = (ids: string[]) => {
    setSlideOrder(ids);
    if (sceneId) void api.patchScene(sceneId, { slideOrder: ids }).catch(() => notice("Não foi possível salvar a ordem."));
  };

  // ------------------------------------------------------------ comments

  const focusComment = (c: Comment) => {
    if (!excalidraw) return;
    const s = excalidraw.getAppState();
    excalidraw.updateScene({
      appState: {
        scrollX: s.width / 2 / s.zoom.value - c.x,
        scrollY: s.height / 2 / s.zoom.value - c.y,
      },
    });
    setOpenThread(c.id);
  };

  const openComments = useMemo(() => threadsOf(comments).filter((t) => !t.root.resolvedAt).length, [comments]);
  const inCallCount = call.peers.size + (call.inCall ? 1 : 0);
  const screens = [...call.peers.entries()].filter(([, p]) => p.screen);

  // ------------------------------------------------------------ render

  if (fatal) {
    return (
      <div className="center-screen">
        <p>{fatal}</p>
        {isMember && (
          <button className="btn" onClick={() => navigate("/")}>
            Voltar aos desenhos
          </button>
        )}
      </div>
    );
  }
  if (!loaded || !initialData) {
    return <div className="center-screen muted">Abrindo o desenho…</div>;
  }

  const isPresenting = presenting !== null;
  const bare = embed || isPresenting;
  const toggle = (p: Panel) => setPanel((cur) => (cur === p ? null : p));

  return (
    <div className={`editor${isPresenting ? " presenting" : ""}`} ref={rootRef}>
      {!bare && (
        <header className="editor-bar">
          {isMember && (
            <button className="icon-btn" title="Voltar aos desenhos" aria-label="Voltar aos desenhos" onClick={() => navigate("/")}>
              ←
            </button>
          )}
          {isMember && canEdit ? (
            <input
              className="title-input"
              value={title}
              aria-label="Título do desenho"
              maxLength={200}
              onChange={(e) => setTitle(e.target.value)}
              onBlur={() => void commitTitle()}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === "Enter") e.currentTarget.blur();
              }}
            />
          ) : (
            <strong className="ellipsis title-static">{title}</strong>
          )}
          <span className={`save-state ${saveState}`} role="status">
            {loaded.scene.deletedAt ? "Na lixeira · somente leitura" : canEdit ? saveLabel[saveState] : "Somente leitura"}
          </span>
          {!online && <span className="save-state error">Reconectando…</span>}
          <span className="grow" />
          {peers.length > 0 && (
            <span className="peers" title={peers.map((p) => p.name).join(", ")}>
              {peers.slice(0, 4).map((p) => (
                <span key={p.id} className="avatar">
                  {p.name.slice(0, 1).toUpperCase()}
                </span>
              ))}
              {peers.length > 4 && <span className="avatar">+{peers.length - 4}</span>}
            </span>
          )}
          {call.inCall ? (
            <>
              <button className={`btn small${call.muted ? "" : " primary"}`} onClick={() => callRef.current?.toggleMute()}>
                {call.muted ? "Mudo" : "Microfone ligado"}
              </button>
              <button className={`btn small${call.sharing ? " primary" : ""}`} onClick={() => void callRef.current?.toggleScreen()}>
                {call.sharing ? "Parar tela" : "Compartilhar tela"}
              </button>
              <button className="btn small danger" onClick={() => callRef.current?.leave()}>
                Sair da chamada ({inCallCount})
              </button>
            </>
          ) : (
            <button className="btn small" onClick={() => void callRef.current?.join()} title="Chamada de voz com quem está no desenho">
              {inCallCount > 0 ? `Entrar na chamada (${inCallCount})` : "Chamada"}
            </button>
          )}
          {access?.canComment && (
            <button className={`btn small${panel === "comments" ? " primary" : ""}`} onClick={() => toggle("comments")}>
              Comentários{openComments > 0 && ` (${openComments})`}
            </button>
          )}
          <button className={`btn small${panel === "slides" ? " primary" : ""}`} onClick={() => toggle("slides")}>
            Slides
          </button>
          {isMember && (
            <>
              <button className={`btn small${panel === "history" ? " primary" : ""}`} onClick={() => toggle("history")}>
                Histórico
              </button>
              <button className="btn small primary" onClick={() => setShareOpen(true)}>
                Compartilhar
              </button>
            </>
          )}
        </header>
      )}
      {call.error && !bare && (
        <div className="banner error">
          {call.error}
          <button className="icon-btn" aria-label="Fechar" onClick={() => setCall((c) => ({ ...c, error: "" }))}>
            ×
          </button>
        </div>
      )}

      <div className="editor-body">
        <div className="canvas">
          <Excalidraw
            excalidrawAPI={setExcalidraw}
            initialData={initialData}
            onChange={onChange}
            onPointerUpdate={onPointerUpdate}
            onLibraryChange={onLibraryChange}
            isCollaborating={peers.length > 0}
            viewModeEnabled={!canEdit || isPresenting}
            zenModeEnabled={isPresenting}
            langCode="pt-BR"
            autoFocus
            handleKeyboardGlobally
            name={title}
            aiEnabled={!!me?.features.ai}
            libraryReturnUrl={location.origin + location.pathname}
            UIOptions={{ canvasActions: { loadScene: canEdit, toggleTheme: true } }}
          >
            <MainMenu>
              {isMember && <MainMenu.Item onSelect={() => navigate("/")}>Voltar aos desenhos</MainMenu.Item>}
              {canEdit && <MainMenu.DefaultItems.LoadScene />}
              <MainMenu.DefaultItems.SaveToActiveFile />
              <MainMenu.DefaultItems.Export />
              <MainMenu.DefaultItems.SaveAsImage />
              <MainMenu.DefaultItems.SearchMenu />
              <MainMenu.Item onSelect={() => startPresenting(0)}>Apresentar</MainMenu.Item>
              <MainMenu.DefaultItems.Help />
              {canEdit && <MainMenu.DefaultItems.ClearCanvas />}
              <MainMenu.Separator />
              <MainMenu.DefaultItems.ToggleTheme />
              <MainMenu.DefaultItems.ChangeCanvasBackground />
            </MainMenu>
            <WelcomeScreen>
              <WelcomeScreen.Hints.ToolbarHint />
              <WelcomeScreen.Hints.MenuHint />
              <WelcomeScreen.Hints.HelpHint />
            </WelcomeScreen>
            {me?.features.ai && (
              <>
                <TTDDialog
                  onTextSubmit={async (prompt) => {
                    try {
                      const { mermaid } = await api.textToDiagram(prompt);
                      return { generatedResponse: mermaid };
                    } catch (e) {
                      return { error: e instanceof Error ? e : new Error("Falha ao gerar o diagrama.") };
                    }
                  }}
                />
                <TTDDialogTrigger />
              </>
            )}
          </Excalidraw>
          {!bare && access?.canComment && (
            <CommentLayer
              comments={comments}
              sceneApi={sceneApi}
              viewport={viewport}
              placing={placing}
              onPlaced={() => setPlacing(false)}
              onChanged={loadComments}
              onError={notice}
              userId={me?.user.id ?? null}
              isAdmin={me?.user.role === "admin"}
              openThread={openThread}
              setOpenThread={setOpenThread}
            />
          )}
        </div>

        {panel && !bare && (
          <aside className="side-panel">
            <div className="row">
              <h2>{panel === "comments" ? "Comentários" : panel === "history" ? "Histórico de versões" : "Apresentação"}</h2>
              <span className="grow" />
              <button className="icon-btn" aria-label="Fechar painel" onClick={() => setPanel(null)}>
                ×
              </button>
            </div>
            {panel === "comments" && (
              <CommentsPanel
                comments={comments}
                canComment={!!access?.canComment}
                placing={placing}
                onPlace={() => setPlacing((v) => !v)}
                onFocus={focusComment}
              />
            )}
            {panel === "history" && sceneId && excalidraw && (
              <HistoryPanel
                sceneId={sceneId}
                canEdit={canEdit}
                files={excalidraw.getFiles}
                flush={() => syncRef.current?.flush() ?? Promise.resolve()}
                onRestored={() => void syncRef.current?.resync().catch(() => undefined)}
                onError={notice}
              />
            )}
            {panel === "slides" && excalidraw && (
              <SlidesPanel
                source={source}
                slides={slides}
                title={title}
                canEdit={canEdit && isMember}
                onReorder={reorderSlides}
                onPresent={startPresenting}
                onError={notice}
              />
            )}
          </aside>
        )}
      </div>

      {screens.map(([id, p]) => (
        <ScreenView key={id} stream={p.screen!} name={peers.find((x) => x.id === id)?.name ?? "Tela"} />
      ))}

      {isPresenting && (
        <PresentBar
          index={presenting}
          total={slides.length}
          name={slides[presenting] ? slideName(slides[presenting], presenting) : ""}
          laser={laser}
          onGo={setPresenting}
          onLaser={() => {
            excalidraw?.setActiveTool({ type: laser ? "selection" : "laser" });
            setLaser(!laser);
          }}
          onExit={stopPresenting}
        />
      )}

      {shareOpen && sceneId && <ShareDialog sceneId={sceneId} canEdit={canEdit} onClose={() => setShareOpen(false)} />}
    </div>
  );
};

/** A collaborator's shared screen, in a window that can be enlarged. */
const ScreenView = ({ stream, name }: { stream: MediaStream; name: string }) => {
  const ref = useRef<HTMLVideoElement>(null);
  const [big, setBig] = useState(false);
  useEffect(() => {
    if (ref.current) ref.current.srcObject = stream;
  }, [stream]);
  return (
    <div className={`screen-view${big ? " big" : ""}`}>
      <div className="row">
        <span className="ellipsis small">Tela de {name}</span>
        <span className="grow" />
        <button className="icon-btn small" onClick={() => setBig(!big)} aria-label={big ? "Reduzir" : "Ampliar"}>
          {big ? "–" : "+"}
        </button>
      </div>
      <video ref={ref} autoPlay playsInline muted />
    </div>
  );
};
