// Voice call and screen sharing between the people in a scene: a WebRTC mesh, with
// the collaboration socket as signalling channel. Media flows peer to peer; the
// server only relays the handshake.

import type { Collab } from "./collab";

export interface CallPeer {
  muted: boolean;
  /** Present while that person shares their screen. */
  screen: MediaStream | null;
}

export interface CallState {
  inCall: boolean;
  muted: boolean;
  sharing: boolean;
  peers: Map<string, CallPeer>;
  error: string;
}

interface Link {
  pc: RTCPeerConnection;
  makingOffer: boolean;
  ignoreOffer: boolean;
  audio: HTMLAudioElement;
}

type Signal =
  | { t: "call"; on: boolean; muted?: boolean }
  | { t: "rtc"; desc?: RTCSessionDescriptionInit; cand?: RTCIceCandidateInit };

export class Call {
  private mic: MediaStream | null = null;
  private screen: MediaStream | null = null;
  private links = new Map<string, Link>();
  state: CallState = { inCall: false, muted: false, sharing: false, peers: new Map(), error: "" };

  constructor(
    private collab: () => Collab | null,
    private iceServers: RTCIceServer[],
    private onChange: (s: CallState) => void,
  ) {}

  private emit(patch: Partial<CallState> = {}) {
    this.state = { ...this.state, ...patch, peers: new Map(this.state.peers) };
    this.onChange(this.state);
  }

  private announce(to?: string) {
    this.collab()?.send({ t: "call", to, on: this.state.inCall, muted: this.state.muted });
  }

  async join() {
    if (this.state.inCall) return;
    try {
      this.mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch {
      this.emit({ error: "Não foi possível acessar o microfone. Verifique a permissão do navegador." });
      return;
    }
    this.emit({ inCall: true, muted: false, error: "" });
    this.announce();
    // People already in the call answer the announcement by connecting to us.
  }

  leave() {
    if (!this.state.inCall) return;
    for (const id of [...this.links.keys()]) this.drop(id);
    this.mic?.getTracks().forEach((t) => t.stop());
    this.screen?.getTracks().forEach((t) => t.stop());
    this.mic = this.screen = null;
    // The others are still talking: keep showing who is in the call.
    for (const [id, peer] of this.state.peers) this.state.peers.set(id, { ...peer, screen: null });
    this.emit({ inCall: false, sharing: false, muted: false });
    this.announce();
  }

  toggleMute() {
    if (!this.mic) return;
    const muted = !this.state.muted;
    this.mic.getAudioTracks().forEach((t) => (t.enabled = !muted));
    this.emit({ muted });
    this.announce();
  }

  async toggleScreen() {
    if (!this.state.inCall) return;
    if (this.screen) {
      this.stopScreen();
      return;
    }
    try {
      this.screen = await navigator.mediaDevices.getDisplayMedia({ video: true });
    } catch {
      return; // the picker was dismissed
    }
    const track = this.screen.getVideoTracks()[0];
    track.onended = () => this.stopScreen();
    for (const link of this.links.values()) link.pc.addTrack(track, this.screen);
    this.emit({ sharing: true });
  }

  private stopScreen() {
    if (!this.screen) return;
    const track = this.screen.getVideoTracks()[0];
    for (const link of this.links.values()) {
      const sender = link.pc.getSenders().find((s) => s.track === track);
      if (sender) link.pc.removeTrack(sender);
    }
    track.stop();
    this.screen = null;
    this.emit({ sharing: false });
  }

  /** A person entered the scene: tell them whether we are in the call. */
  peerJoined(id: string) {
    if (this.state.inCall) this.announce(id);
  }

  peerLeft(id: string) {
    this.drop(id);
    if (this.state.peers.delete(id)) this.emit();
  }

  async handle(from: string, msg: Signal) {
    if (msg.t === "call") {
      if (!msg.on) {
        this.peerLeft(from);
        return;
      }
      const known = this.state.peers.get(from);
      this.state.peers.set(from, { muted: !!msg.muted, screen: known?.screen ?? null });
      this.emit();
      if (this.state.inCall && !this.links.has(from)) {
        this.link(from);
        // They may have announced before we joined: make sure they know about us.
        if (!known) this.announce(from);
      }
      return;
    }
    if (!this.state.inCall) return;
    const link = this.links.get(from) ?? this.link(from);
    const { pc } = link;
    try {
      if (msg.desc) {
        // Perfect negotiation: on a collision the polite side yields its own offer.
        const polite = (this.collab()?.selfId ?? "") < from;
        const collision = msg.desc.type === "offer" && (link.makingOffer || pc.signalingState !== "stable");
        link.ignoreOffer = !polite && collision;
        if (link.ignoreOffer) return;
        await pc.setRemoteDescription(msg.desc);
        if (msg.desc.type === "offer") {
          await pc.setLocalDescription();
          this.collab()?.send({ t: "rtc", to: from, desc: pc.localDescription });
        }
      } else if (msg.cand) {
        try {
          await pc.addIceCandidate(msg.cand);
        } catch (e) {
          if (!link.ignoreOffer) throw e;
        }
      }
    } catch (e) {
      console.warn("call signalling failed", e);
    }
  }

  private link(id: string): Link {
    const pc = new RTCPeerConnection({ iceServers: this.iceServers });
    const audio = new Audio();
    audio.autoplay = true;
    const link: Link = { pc, makingOffer: false, ignoreOffer: false, audio };
    this.links.set(id, link);

    pc.onnegotiationneeded = async () => {
      try {
        link.makingOffer = true;
        await pc.setLocalDescription();
        this.collab()?.send({ t: "rtc", to: id, desc: pc.localDescription });
      } catch (e) {
        console.warn("call offer failed", e);
      } finally {
        link.makingOffer = false;
      }
    };
    pc.onicecandidate = ({ candidate }) => {
      if (candidate) this.collab()?.send({ t: "rtc", to: id, cand: candidate.toJSON() });
    };
    pc.ontrack = ({ track, streams }) => {
      if (track.kind === "audio") {
        audio.srcObject = streams[0] ?? new MediaStream([track]);
        void audio.play().catch(() => undefined);
        return;
      }
      const stream = streams[0] ?? new MediaStream([track]);
      const setScreen = (screen: MediaStream | null) => {
        const p = this.state.peers.get(id);
        if (p) {
          this.state.peers.set(id, { ...p, screen });
          this.emit();
        }
      };
      setScreen(stream);
      stream.onremovetrack = () => setScreen(null);
      track.onended = () => setScreen(null);
    };
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === "failed") pc.restartIce();
    };

    for (const track of this.mic?.getTracks() ?? []) pc.addTrack(track, this.mic!);
    for (const track of this.screen?.getTracks() ?? []) pc.addTrack(track, this.screen!);
    return link;
  }

  private drop(id: string) {
    const link = this.links.get(id);
    if (!link) return;
    link.pc.close();
    link.audio.srcObject = null;
    this.links.delete(id);
  }

  dispose() {
    this.leave();
  }
}
