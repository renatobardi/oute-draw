package scene

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
)

func nonce() int64 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint32(b[:]) >> 1)
}

func asInt(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return 0
}

// RestoreTo returns the element list that makes every client converge on snapshot.
// A plain overwrite would lose to Merge, so each restored element gets a version
// above the current one, and current elements missing from the snapshot become
// tombstones with a bumped version.
func RestoreTo(current, snapshot []json.RawMessage) []json.RawMessage {
	curVersion := map[string]int64{}
	for _, raw := range current {
		if m, ok := parse(raw); ok {
			curVersion[m.ID] = m.Version
		}
	}
	inSnapshot := map[string]bool{}
	out := make([]json.RawMessage, 0, len(snapshot)+len(current))
	bump := func(raw json.RawMessage, deleted *bool) (json.RawMessage, string) {
		var el map[string]any
		if err := json.Unmarshal(raw, &el); err != nil {
			return nil, ""
		}
		id, _ := el["id"].(string)
		if id == "" {
			return nil, ""
		}
		v := asInt(el["version"])
		if cv := curVersion[id]; cv > v {
			v = cv
		}
		el["version"] = v + 1
		el["versionNonce"] = nonce()
		if deleted != nil {
			el["isDeleted"] = *deleted
		}
		b, err := json.Marshal(el)
		if err != nil {
			return nil, ""
		}
		return b, id
	}
	for _, raw := range snapshot {
		if b, id := bump(raw, nil); b != nil {
			inSnapshot[id] = true
			out = append(out, b)
		}
	}
	gone := true
	for _, raw := range current {
		m, ok := parse(raw)
		if !ok || inSnapshot[m.ID] {
			continue
		}
		if m.IsDeleted {
			out = append(out, raw)
			continue
		}
		if b, _ := bump(raw, &gone); b != nil {
			out = append(out, b)
		}
	}
	return out
}
