// The live channel of a scene: who is here, what they are changing, where they point.
// It only relays; saving goes through HTTP, so a dropped socket never loses work.

export interface Peer {
  id: string;
  name: string;
  canEdit: boolean;
}

export type ServerEvent =
  | { t: "hello"; id: string; peers: Peer[] }
  | { t: "join"; peer: Peer }
  | { t: "leave"; id: string }
  | { t: "relay"; from: string; m: { t: string; [k: string]: unknown } }
  | { t: "comments" }
  | { t: "reload" }
  | { t: "meta" };

export class Collab {
  private ws: WebSocket | null = null;
  private closed = false;
  private retry = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  selfId = "";

  constructor(
    private url: () => string,
    private onEvent: (e: ServerEvent) => void,
    private onStatus: (online: boolean) => void,
  ) {
    this.connect();
  }

  private connect() {
    if (this.closed) return;
    const ws = new WebSocket(this.url());
    this.ws = ws;
    ws.onmessage = (ev) => {
      let msg: ServerEvent;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (msg.t === "hello") {
        this.selfId = msg.id;
        this.retry = 0;
        this.onStatus(true);
      }
      this.onEvent(msg);
    };
    ws.onclose = () => {
      if (this.ws !== ws) return;
      this.ws = null;
      this.onStatus(false);
      if (this.closed) return;
      // Back off up to 15 s; a fresh "hello" makes the editor resync the scene.
      const delay = Math.min(15000, 500 * 2 ** this.retry++);
      this.timer = setTimeout(() => this.connect(), delay);
    };
  }

  get online() {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  /** Sends to everyone else in the scene, or to one peer when `to` is given. */
  send(msg: { t: string; to?: string; [k: string]: unknown }) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
    }
  }

  close() {
    this.closed = true;
    clearTimeout(this.timer);
    this.ws?.close();
  }
}
