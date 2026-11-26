package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/renatobardi/oute-draw/internal/store"
)

const ttdSystem = `You turn a description into one Mermaid diagram for a whiteboard.
Reply with the Mermaid source only: no code fences, no commentary before or after.
Prefer flowchart, sequenceDiagram or classDiagram, the types the whiteboard converts
into editable shapes. Keep labels short and in the language of the request.`

// handleTextToDiagram asks Claude for a Mermaid diagram; the editor converts it to
// shapes. The feature is off unless ANTHROPIC_API_KEY is configured.
func (s *Server) handleTextToDiagram(w http.ResponseWriter, r *http.Request, _ store.User) {
	if s.cfg.AnthropicAPIKey == "" {
		writeError(w, http.StatusNotImplemented, "AI is not configured on this server")
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if len(prompt) < 3 || len(prompt) > 4000 {
		writeError(w, http.StatusBadRequest, "prompt must have between 3 and 4000 characters")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	client := anthropic.NewClient(option.WithAPIKey(s.cfg.AnthropicAPIKey), option.WithHTTPClient(s.http))
	// A declined request is re-served by the fallback model inside the same call.
	resp, err := client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:        s.cfg.AIModel,
		MaxTokens:    16000,
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks:    anthropic.BetaFallbacksParamOfDefault(),
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortLow},
		System:       []anthropic.BetaTextBlockParam{{Text: ttdSystem}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(prompt)),
		},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			s.log.Error("ai request", "status", apiErr.StatusCode, "err", err)
			switch apiErr.StatusCode {
			case http.StatusTooManyRequests:
				writeError(w, http.StatusTooManyRequests, "AI rate limit reached, try again shortly")
			case http.StatusUnauthorized, http.StatusForbidden:
				writeError(w, http.StatusBadGateway, "AI credentials were rejected")
			default:
				writeError(w, http.StatusBadGateway, "AI request failed")
			}
			return
		}
		s.log.Error("ai request", "err", err)
		writeError(w, http.StatusBadGateway, "AI is unreachable")
		return
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		writeError(w, http.StatusUnprocessableEntity, "the model declined this request")
		return
	}
	var out strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			out.WriteString(t.Text)
		}
	}
	mermaid := strings.TrimSpace(out.String())
	// The prompt asks for bare Mermaid; tolerate a fenced reply all the same.
	if strings.HasPrefix(mermaid, "```") {
		if _, rest, ok := strings.Cut(mermaid, "\n"); ok {
			mermaid = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "```"))
		}
	}
	if mermaid == "" {
		writeError(w, http.StatusBadGateway, "AI returned no diagram")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mermaid": mermaid})
}
