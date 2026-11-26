package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/renatobardi/oute-draw/internal/hub"
	"github.com/renatobardi/oute-draw/internal/store"
)

// Messages that change the drawing need edit access; the rest (cursors, call
// signalling) is open to everyone in the room.
var editOnly = map[string]bool{"update": true}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request, sceneID string, a actor) {
	// Accept checks that Origin matches Host, so another site cannot open the socket
	// with the visitor's session.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck
	conn.SetReadLimit(16 << 20)

	client := s.hub.Join(sceneID, hub.Peer{ID: store.NewID(), Name: a.Name, CanEdit: a.CanEdit})
	defer s.hub.Leave(sceneID, client)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-client.Send:
				if !ok {
					_ = conn.Close(websocket.StatusPolicyViolation, "too slow")
					return
				}
				wctx, done := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, msg)
				done()
				if err != nil {
					return
				}
			case <-ping.C:
				pctx, done := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pctx)
				done()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var head struct {
			T  string `json:"t"`
			To string `json:"to"`
		}
		if json.Unmarshal(data, &head) != nil || head.T == "" {
			continue
		}
		if editOnly[head.T] && !a.CanEdit {
			continue
		}
		s.hub.Relay(sceneID, client, head.To, data)
	}
}
