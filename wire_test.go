package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWirePreservesPathsAndBudgets(t *testing.T) {
	t.Parallel()

	decoded, err := decodeWire([]byte(`{"path":"/tmp/\udcff","budget":90071992547409931234567890,"nested":[true,null]}`))
	if err != nil {
		t.Fatal(err)
	}

	object, ok := decoded.(map[string]any)
	if !ok {
		t.Fatal("object required")
	}

	path, ok := object["path"].(string)
	if !ok || path != "/tmp/\xff" {
		t.Fatalf("path bytes lost: %q", path)
	}

	encoded, err := json.Marshal(exactNumbers(decoded))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(encoded, []byte(`\udcff`)) || !bytes.Contains(encoded, []byte(`"budget":"90071992547409931234567890"`)) {
		t.Fatalf("wire values lost: %s", encoded)
	}
}

func TestWireRejectsPartialResponse(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", `{"path":`, "{} extra"} {
		if _, err := decodeWire([]byte(value)); err == nil {
			t.Fatalf("accepted incomplete response %q", value)
		}
	}
}
