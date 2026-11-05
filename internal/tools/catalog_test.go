package tools

import (
	"encoding/json"
	"testing"
)

func TestStickerValuesKeepBothStickersOfOneName(t *testing.T) {
	catalog := map[string]apiSticker{
		"a": {ID: "a", Name: "Sprint", States: []apiState{{"x", "One"}}},
		"b": {ID: "b", Name: "Sprint"},
	}
	got, _ := json.Marshal(stickerValues(map[string]any{"a": "x", "b": "free", "c": "empty"}, catalog))
	if string(got) != `{"Sprint":"One","Sprint (b)":"free","c":"empty"}` {
		t.Errorf("got %s", got)
	}
}
