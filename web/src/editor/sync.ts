import { CaptureUpdateAction, exportToBlob, reconcileElements, restoreElements } from "@excalidraw/excalidraw";
import type { ExcalidrawElement, OrderedExcalidrawElement } from "@excalidraw/excalidraw/element/types";
import type { RemoteExcalidrawElement } from "@excalidraw/excalidraw/data/reconcile";
import type { AppState, BinaryFileData, BinaryFiles, ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types";

import type { SceneApi } from "../api";
import { pickAppState } from "../pages/Dashboard";
import type { Collab } from "./collab";

export type SaveState = "saved" | "dirty" | "saving" | "error";

const SAVE_DELAY = 1200;
const BROADCAST_DELAY = 40;
const THUMBNAIL_EVERY = 20_000;

const blobToDataURL = (blob: Blob) =>
  new Promise<string>((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result as string);
    r.onerror = () => reject(r.error);
    r.readAsDataURL(blob);
  });

/**
 * Keeps one open scene in step with the server and with the other people in it.
 *
 * Two independent paths, both keyed on element versions:
 *  - saving: changed elements go over HTTP and the server merges them, so two
 *    editors can save at once without losing each other's work;
 *  - live relay: changed elements go over the socket for the others to see now.
 * Neither path sends an element whose version it has already sent.
 */
export class SceneSync {
  /** Element versions the server has acknowledged. */
  private saved = new Map<string, number>();
  /** Element versions already relayed (or received) on the socket. */
  private sent = new Map<string, number>();
  private savedAppState = "";
  private knownFiles = new Set<string>();
  private uploads = new Set<Promise<void>>();

  private elements: readonly OrderedExcalidrawElement[] = [];
  private appState: AppState | null = null;
  private files: BinaryFiles = {};

  private saveTimer: ReturnType<typeof setTimeout> | undefined;
  private relayTimer: ReturnType<typeof setTimeout> | undefined;
  private saving = false;
  private lastThumbnail = 0;
  private disposed = false;

  constructor(
    private excalidraw: ExcalidrawImperativeAPI,
    private sceneApi: SceneApi,
    private canEdit: boolean,
    private collab: () => Collab | null,
    private onState: (s: SaveState) => void,
    private onNotice: (message: string) => void,
  ) {}

  /** Marks what was loaded from the server as already saved and already relayed. */
  seed(elements: readonly ExcalidrawElement[], appState: Record<string, unknown>, fileIds: string[]) {
    for (const el of elements) {
      this.saved.set(el.id, el.version);
      this.sent.set(el.id, el.version);
    }
    this.savedAppState = JSON.stringify(pickAppState(appState));
    void this.loadFiles(fileIds);
  }

  onChange(elements: readonly OrderedExcalidrawElement[], appState: AppState, files: BinaryFiles) {
    this.elements = elements;
    this.appState = appState;
    this.files = files;
    if (!this.canEdit || this.disposed) return;

    this.uploadNewFiles();
    if (!this.relayTimer) {
      this.relayTimer = setTimeout(() => {
        this.relayTimer = undefined;
        this.relay();
      }, BROADCAST_DELAY);
    }
    if (this.isDirty()) {
      if (!this.saving) this.onState("dirty");
      clearTimeout(this.saveTimer);
      this.saveTimer = setTimeout(() => void this.save(), SAVE_DELAY);
    }
  }

  private isDirty() {
    for (const el of this.elements) {
      if (this.saved.get(el.id) !== el.version) return true;
    }
    return this.appState !== null && JSON.stringify(pickAppState(this.appState)) !== this.savedAppState;
  }

  private relay() {
    const collab = this.collab();
    if (!collab?.online) return;
    const changed = this.elements.filter((el) => this.sent.get(el.id) !== el.version);
    if (!changed.length) return;
    collab.send({ t: "update", elements: changed });
    for (const el of changed) this.sent.set(el.id, el.version);
  }

  /** Someone joined: they loaded the saved scene, which may lag what is on screen. */
  relayAll() {
    const collab = this.collab();
    if (this.canEdit && collab?.online && this.elements.length) {
      const unsaved = this.elements.filter((el) => this.saved.get(el.id) !== el.version);
      if (unsaved.length) collab.send({ t: "update", elements: unsaved });
    }
  }

  /** Merges elements that came from a collaborator or from the server. */
  applyRemote(raw: unknown[]) {
    const remote = restoreElements(raw as ExcalidrawElement[], null);
    const local = this.excalidraw.getSceneElementsIncludingDeleted();
    const merged = reconcileElements(
      local as readonly OrderedExcalidrawElement[],
      remote as RemoteExcalidrawElement[],
      this.excalidraw.getAppState(),
    );
    for (const el of remote) this.sent.set(el.id, el.version);
    this.excalidraw.updateScene({ elements: merged, captureUpdate: CaptureUpdateAction.NEVER });
    const fileIds = remote.flatMap((el) => (el.type === "image" && el.fileId && !el.isDeleted ? [el.fileId] : []));
    if (fileIds.length) void this.loadFiles(fileIds);
  }

  /** Pulls the stored scene and merges it in: after a reconnect or a version restore. */
  async resync() {
    const { scene } = await this.sceneApi.load();
    for (const el of scene.elements as ExcalidrawElement[]) this.saved.set(el.id, el.version);
    this.applyRemote(scene.elements);
    void this.loadFiles(scene.fileIds);
    return scene;
  }

  private async loadFiles(ids: string[], attempt = 0): Promise<void> {
    const missing = ids.filter((id) => !this.knownFiles.has(id));
    if (!missing.length || this.disposed) return;
    for (const id of missing) this.knownFiles.add(id);
    const failed: string[] = [];
    const loaded: BinaryFileData[] = [];
    await Promise.all(
      missing.map(async (id) => {
        try {
          const blob = await this.sceneApi.getFile(id);
          loaded.push({
            id: id as BinaryFileData["id"],
            dataURL: (await blobToDataURL(blob)) as BinaryFileData["dataURL"],
            mimeType: blob.type as BinaryFileData["mimeType"],
            created: Date.now(),
          });
        } catch {
          this.knownFiles.delete(id);
          failed.push(id);
        }
      }),
    );
    if (loaded.length && !this.disposed) this.excalidraw.addFiles(loaded);
    // A collaborator's image can be announced before its upload lands: try again.
    if (failed.length && attempt < 5) {
      setTimeout(() => void this.loadFiles(failed, attempt + 1), 1500 * (attempt + 1));
    }
  }

  private uploadNewFiles() {
    for (const el of this.elements) {
      if (el.type !== "image" || el.isDeleted || !el.fileId || this.knownFiles.has(el.fileId)) continue;
      const file = this.files[el.fileId];
      if (!file) continue;
      const fileId = el.fileId;
      this.knownFiles.add(fileId);
      const upload = fetch(file.dataURL)
        .then((r) => r.blob())
        .then((blob) => this.sceneApi.putFile(fileId, blob))
        .catch((e: Error) => {
          this.knownFiles.delete(fileId);
          this.onNotice(`Não foi possível enviar a imagem: ${e.message}`);
        })
        .finally(() => this.uploads.delete(upload));
      this.uploads.add(upload);
    }
  }

  private async thumbnail(): Promise<string | undefined> {
    const visible = this.elements.filter((el) => !el.isDeleted);
    if (!visible.length || !this.appState) return undefined;
    try {
      const blob = await exportToBlob({
        elements: visible,
        appState: { ...this.appState, exportBackground: true, exportWithDarkMode: false },
        files: this.files,
        maxWidthOrHeight: 480,
        mimeType: "image/png",
      });
      return await blobToDataURL(blob);
    } catch {
      return undefined; // a preview is never worth failing a save for
    }
  }

  private async save(opts: { keepalive?: boolean; forceThumbnail?: boolean } = {}): Promise<void> {
    clearTimeout(this.saveTimer);
    if (!this.canEdit) return;
    if (this.saving) {
      // The running save will notice the scene is still dirty and go again.
      return;
    }
    if (!this.isDirty() && !opts.forceThumbnail) return;
    this.saving = true;
    this.onState("saving");
    const batch = this.elements.filter((el) => this.saved.get(el.id) !== el.version);
    const appStateJSON = this.appState ? JSON.stringify(pickAppState(this.appState)) : this.savedAppState;
    try {
      if (!opts.keepalive) await Promise.allSettled([...this.uploads]);
      let thumbnail: string | undefined;
      if (!opts.keepalive && (opts.forceThumbnail || Date.now() - this.lastThumbnail > THUMBNAIL_EVERY)) {
        thumbnail = await this.thumbnail();
        this.lastThumbnail = Date.now();
      }
      await this.sceneApi.save(
        {
          elements: batch,
          appState: appStateJSON !== this.savedAppState ? JSON.parse(appStateJSON) : undefined,
          thumbnail,
        },
        opts.keepalive,
      );
      for (const el of batch) this.saved.set(el.id, el.version);
      this.savedAppState = appStateJSON;
      this.saving = false;
      if (this.isDirty()) {
        this.onState("dirty");
        this.saveTimer = setTimeout(() => void this.save(), SAVE_DELAY);
      } else {
        this.onState("saved");
      }
    } catch {
      this.saving = false;
      this.onState("error");
      if (!this.disposed) this.saveTimer = setTimeout(() => void this.save(), 5000);
    }
  }

  /** Saves now. With keepalive the request survives the page going away. */
  flush(opts: { keepalive?: boolean; forceThumbnail?: boolean } = {}) {
    return this.save(opts);
  }

  dispose() {
    this.disposed = true;
    clearTimeout(this.saveTimer);
    clearTimeout(this.relayTimer);
  }
}
