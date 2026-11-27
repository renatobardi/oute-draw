package scene

import (
	"encoding/json"
	"testing"
)

func els(t *testing.T, items ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(items))
	for i, s := range items {
		out[i] = json.RawMessage(s)
	}
	return out
}

func ids(t *testing.T, list []json.RawMessage) []string {
	t.Helper()
	var out []string
	for _, raw := range list {
		var m struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m.ID)
	}
	return out
}

func TestMergeHigherVersionWins(t *testing.T) {
	stored := els(t, `{"id":"a","version":3,"versionNonce":1,"index":"a0","x":1}`)
	merged, changed := Merge(stored, els(t, `{"id":"a","version":2,"versionNonce":1,"index":"a0","x":9}`))
	if changed || string(merged[0]) != string(stored[0]) {
		t.Fatalf("stale element replaced the stored one: %s", merged[0])
	}
	merged, changed = Merge(stored, els(t, `{"id":"a","version":4,"versionNonce":7,"index":"a0","x":9}`))
	if !changed || string(merged[0]) != `{"id":"a","version":4,"versionNonce":7,"index":"a0","x":9}` {
		t.Fatalf("newer element did not win: %s", merged[0])
	}
}

func TestMergeTieBreaksOnLowerNonce(t *testing.T) {
	stored := els(t, `{"id":"a","version":3,"versionNonce":50}`)
	if _, changed := Merge(stored, els(t, `{"id":"a","version":3,"versionNonce":90}`)); changed {
		t.Fatal("higher nonce must lose the tie")
	}
	if _, changed := Merge(stored, els(t, `{"id":"a","version":3,"versionNonce":10}`)); !changed {
		t.Fatal("lower nonce must win the tie")
	}
}

func TestMergeKeepsTombstonesAndOrdersByIndex(t *testing.T) {
	stored := els(t,
		`{"id":"a","version":1,"versionNonce":1,"index":"a0"}`,
		`{"id":"c","version":1,"versionNonce":1,"index":"a2"}`,
	)
	merged, changed := Merge(stored, els(t,
		`{"id":"b","version":1,"versionNonce":1,"index":"a1"}`,
		`{"id":"a","version":2,"versionNonce":1,"index":"a0","isDeleted":true}`,
	))
	if !changed {
		t.Fatal("expected a change")
	}
	got := ids(t, merged)
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	if Live(merged) != 2 {
		t.Fatalf("live = %d, want 2", Live(merged))
	}
}

func TestMergeIgnoresGarbage(t *testing.T) {
	merged, changed := Merge(nil, els(t, `"nope"`, `{"version":1}`, `{"id":"ok","version":1}`))
	if !changed || len(merged) != 1 {
		t.Fatalf("merged = %v", merged)
	}
}

func TestText(t *testing.T) {
	got := Text(els(t,
		`{"id":"a","type":"text","text":" hello "}`,
		`{"id":"b","type":"text","text":"gone","isDeleted":true}`,
		`{"id":"c","type":"frame","name":"Slide 1"}`,
		`{"id":"d","type":"rectangle"}`,
	))
	if got != "hello\nSlide 1\n" {
		t.Fatalf("text = %q", got)
	}
}
