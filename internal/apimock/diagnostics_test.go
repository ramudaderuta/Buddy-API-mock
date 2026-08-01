package apimock

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDiagnosticsReturnsOnlySafeRuntimeMetadata(t *testing.T) {
	a := &app{state: state{
		Strategy: "fill_first",
		Accounts: []Account{
			{ID: "enabled", Key: "private-key", Enabled: true},
			{ID: "disabled", Key: "other-private-key", Enabled: false},
		},
		Records: []Record{{ID: "record"}},
	}}
	recorder := httptest.NewRecorder()
	a.diagnostics(recorder, nil)

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["accountsTotal"] != float64(2) || body["accountsAvailable"] != float64(1) || body["records"] != float64(1) {
		t.Fatalf("unexpected diagnostics: %#v", body)
	}
	if _, exists := body["accounts"]; exists {
		t.Fatal("diagnostics must not expose account details")
	}
	if _, exists := body["apiKey"]; exists {
		t.Fatal("diagnostics must not expose credentials")
	}
}
