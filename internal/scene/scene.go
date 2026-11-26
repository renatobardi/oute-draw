// Package scene holds the server-side knowledge about drawing elements: just enough
// to merge concurrent saves and to index text, never to render.
package scene

import (
	"encoding/json"
	"sort"
	"strings"
)

// meta is the subset of an element the server reads. Everything else is opaque and
// round-trips untouched.
type meta struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	Version      int64   `json:"version"`
	VersionNonce int64   `json:"versionNonce"`
	Index        *string `json:"index"`
	IsDeleted    bool    `json:"isDeleted"`
	Text         string  `json:"text"`
	Name         *string `json:"name"`
}

func parse(raw json.RawMessage) (meta, bool) {
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" {
		return m, false
	}
	return m, true
}

// Merge reconciles an incoming element list into the stored one. Per element the
// higher version wins and, on a tie, the lower versionNonce: the same rule the
// editor applies between collaborators, so every client and the server converge.
// Deleted elements stay as tombstones, otherwise a stale client would resurrect them.
// It reports whether the stored list changed.
func Merge(stored, incoming []json.RawMessage) ([]json.RawMessage, bool) {
	type entry struct {
		raw  json.RawMessage
		meta meta
		pos  int
	}
	byID := make(map[string]*entry, len(stored)+len(incoming))
	order := make([]*entry, 0, len(stored)+len(incoming))
	for _, raw := range stored {
		m, ok := parse(raw)
		if !ok {
			continue
		}
		if _, dup := byID[m.ID]; dup {
			continue
		}
		e := &entry{raw: raw, meta: m, pos: len(order)}
		byID[m.ID] = e
		order = append(order, e)
	}
	changed := false
	for _, raw := range incoming {
		m, ok := parse(raw)
		if !ok {
			continue
		}
		cur, exists := byID[m.ID]
		if !exists {
			e := &entry{raw: raw, meta: m, pos: len(order)}
			byID[m.ID] = e
			order = append(order, e)
			changed = true
			continue
		}
		if m.Version > cur.meta.Version ||
			(m.Version == cur.meta.Version && m.VersionNonce < cur.meta.VersionNonce) {
			cur.raw, cur.meta = raw, m
			changed = true
		}
	}
	// Z-order is the fractional index; elements without one keep arrival order.
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i].meta.Index, order[j].meta.Index
		if a == nil || b == nil || *a == *b {
			return order[i].pos < order[j].pos
		}
		return *a < *b
	})
	out := make([]json.RawMessage, len(order))
	for i, e := range order {
		out[i] = e.raw
	}
	return out, changed
}

// Text returns the searchable text of a scene: text elements and frame names.
func Text(elements []json.RawMessage) string {
	var b strings.Builder
	for _, raw := range elements {
		m, ok := parse(raw)
		if !ok || m.IsDeleted {
			continue
		}
		s := m.Text
		if s == "" && m.Name != nil {
			s = *m.Name
		}
		if s = strings.TrimSpace(s); s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Live counts the elements that are not tombstones.
func Live(elements []json.RawMessage) int {
	n := 0
	for _, raw := range elements {
		if m, ok := parse(raw); ok && !m.IsDeleted {
			n++
		}
	}
	return n
}
