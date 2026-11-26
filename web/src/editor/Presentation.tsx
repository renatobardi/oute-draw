import { exportToCanvas } from "@excalidraw/excalidraw";
import type { ExcalidrawElement, ExcalidrawFrameLikeElement } from "@excalidraw/excalidraw/element/types";
import type { AppState, BinaryFiles } from "@excalidraw/excalidraw/types";
import { useEffect, useMemo, useState } from "react";

export type Frame = ExcalidrawFrameLikeElement;

/**
 * The slides of a scene are its frames. The saved order comes first; frames not in
 * it yet (new ones) follow in reading order, top to bottom then left to right.
 */
export const slidesOf = (elements: readonly ExcalidrawElement[], order: string[]): Frame[] => {
  const frames = elements.filter((el): el is Frame => el.type === "frame" && !el.isDeleted);
  const rank = new Map(order.map((id, i) => [id, i]));
  return [...frames].sort((a, b) => {
    const ra = rank.get(a.id);
    const rb = rank.get(b.id);
    if (ra !== undefined && rb !== undefined) return ra - rb;
    if (ra !== undefined) return -1;
    if (rb !== undefined) return 1;
    return Math.abs(a.y - b.y) > Math.min(a.height, b.height) / 2 ? a.y - b.y : a.x - b.x;
  });
};

export const slideName = (f: Frame, i: number) => f.name?.trim() || `Slide ${i + 1}`;

interface Source {
  elements: readonly ExcalidrawElement[];
  appState: AppState;
  files: BinaryFiles;
}

const renderSlide = (src: Source, frame: Frame, scale: number) =>
  exportToCanvas({
    elements: src.elements.filter((el) => !el.isDeleted),
    appState: { ...src.appState, exportBackground: true, exportWithDarkMode: false },
    files: src.files,
    exportingFrame: frame,
    exportPadding: 0,
    getDimensions: (width: number, height: number) => ({ width: width * scale, height: height * scale, scale }),
  });

const safeName = (title: string) => title.replace(/[\\/:*?"<>|]+/g, " ").trim() || "apresentacao";

/** One PDF page per slide, each page the size of its frame. */
export const exportPdf = async (src: Source, slides: Frame[], title: string) => {
  const { jsPDF } = await import("jspdf");
  let doc: InstanceType<typeof jsPDF> | null = null;
  for (const frame of slides) {
    const canvas = await renderSlide(src, frame, 2);
    const size: [number, number] = [frame.width, frame.height];
    const orientation = frame.width >= frame.height ? "landscape" : "portrait";
    if (!doc) {
      doc = new jsPDF({ unit: "px", format: size, orientation, hotfixes: ["px_scaling"], compress: true });
    } else {
      doc.addPage(size, orientation);
    }
    doc.addImage(canvas.toDataURL("image/jpeg", 0.92), "JPEG", 0, 0, frame.width, frame.height);
  }
  doc?.save(`${safeName(title)}.pdf`);
};

/** One 16:9 slide per frame, the drawing centred and scaled to fit. */
export const exportPptx = async (src: Source, slides: Frame[], title: string) => {
  const { default: Pptx } = await import("pptxgenjs");
  const deck = new Pptx();
  const W = 13.333;
  const H = 7.5;
  deck.defineLayout({ name: "WIDE", width: W, height: H });
  deck.layout = "WIDE";
  deck.title = title;
  for (const [i, frame] of slides.entries()) {
    const canvas = await renderSlide(src, frame, 2);
    const fit = Math.min(W / frame.width, H / frame.height);
    const w = frame.width * fit;
    const h = frame.height * fit;
    const slide = deck.addSlide();
    slide.background = { color: (src.appState.viewBackgroundColor || "#ffffff").replace("#", "").slice(0, 6) };
    slide.addImage({ data: canvas.toDataURL("image/png"), x: (W - w) / 2, y: (H - h) / 2, w, h });
    slide.addNotes(slideName(frame, i));
  }
  await deck.writeFile({ fileName: `${safeName(title)}.pptx` });
};

interface PanelProps {
  source: () => Source;
  slides: Frame[];
  title: string;
  canEdit: boolean;
  onReorder: (ids: string[]) => void;
  onPresent: (index: number) => void;
  onError: (message: string) => void;
}

export const SlidesPanel = (p: PanelProps) => {
  const [thumbs, setThumbs] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  // Redraw the previews only when a frame itself changes, not on every stroke.
  const key = useMemo(() => p.slides.map((f) => `${f.id}:${f.version}`).join(","), [p.slides]);

  useEffect(() => {
    let live = true;
    const t = setTimeout(async () => {
      const src = p.source();
      const next: Record<string, string> = {};
      for (const frame of p.slides) {
        try {
          const scale = Math.min(1, 320 / Math.max(frame.width, 1));
          next[frame.id] = (await renderSlide(src, frame, scale)).toDataURL("image/jpeg", 0.8);
        } catch {
          /* a slide without preview still lists by name */
        }
        if (!live) return;
      }
      setThumbs(next);
    }, 150);
    return () => {
      live = false;
      clearTimeout(t);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const move = (i: number, by: number) => {
    const ids = p.slides.map((f) => f.id);
    const j = i + by;
    if (j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    p.onReorder(ids);
  };

  const run = async (kind: "pdf" | "pptx") => {
    setBusy(kind);
    try {
      await (kind === "pdf" ? exportPdf : exportPptx)(p.source(), p.slides, p.title);
    } catch {
      p.onError("Não foi possível exportar a apresentação.");
    }
    setBusy("");
  };

  if (!p.slides.length) {
    return (
      <p className="muted">
        Cada <strong>frame</strong> do desenho é um slide. Use a ferramenta de frame (tecla <kbd>F</kbd>) para
        enquadrar o que entra em cada um.
      </p>
    );
  }
  return (
    <>
      <div className="row wrap">
        <button className="btn primary" onClick={() => p.onPresent(0)}>
          Apresentar
        </button>
        <button className="btn" disabled={!!busy} onClick={() => void run("pdf")}>
          {busy === "pdf" ? "Gerando…" : "PDF"}
        </button>
        <button className="btn" disabled={!!busy} onClick={() => void run("pptx")}>
          {busy === "pptx" ? "Gerando…" : "PPTX"}
        </button>
      </div>
      <div className="list">
        {p.slides.map((f, i) => (
          <div key={f.id} className="slide">
            <button className="slide-thumb" onClick={() => p.onPresent(i)} title="Apresentar a partir daqui">
              {thumbs[f.id] ? <img src={thumbs[f.id]} alt="" /> : <span className="muted small">…</span>}
            </button>
            <div className="row">
              <span className="muted small">{i + 1}</span>
              <span className="ellipsis">{slideName(f, i)}</span>
              <span className="grow" />
              {p.canEdit && (
                <>
                  <button className="icon-btn small" disabled={i === 0} title="Mover para cima" onClick={() => move(i, -1)}>
                    ↑
                  </button>
                  <button
                    className="icon-btn small"
                    disabled={i === p.slides.length - 1}
                    title="Mover para baixo"
                    onClick={() => move(i, 1)}
                  >
                    ↓
                  </button>
                </>
              )}
            </div>
          </div>
        ))}
      </div>
    </>
  );
};

export const PresentBar = (p: {
  index: number;
  total: number;
  name: string;
  laser: boolean;
  onGo: (index: number) => void;
  onLaser: () => void;
  onExit: () => void;
}) => (
  <div className="present-bar">
    <button className="icon-btn" disabled={p.index === 0} aria-label="Slide anterior" onClick={() => p.onGo(p.index - 1)}>
      ←
    </button>
    <span className="present-count" title={p.name}>
      {p.index + 1} / {p.total}
    </span>
    <button
      className="icon-btn"
      disabled={p.index >= p.total - 1}
      aria-label="Próximo slide"
      onClick={() => p.onGo(p.index + 1)}
    >
      →
    </button>
    <button className={`btn small${p.laser ? " primary" : " ghost"}`} onClick={p.onLaser}>
      Laser
    </button>
    <button className="btn ghost small" onClick={p.onExit}>
      Sair
    </button>
  </div>
);
