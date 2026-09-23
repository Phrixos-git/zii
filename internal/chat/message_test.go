package chat

import (
	"encoding/json"
	"testing"
)

func TestMessageJSONTags(t *testing.T) {
	encoded, err := json.Marshal(Message{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatalf("marshal Message: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal marshaled Message into map[string]string: %v", err)
	}

	if got, want := decoded["role"], "user"; got != want {
		t.Errorf("JSON key role = %q, want %q", got, want)
	}
	if got, want := decoded["content"], "hello"; got != want {
		t.Errorf("JSON key content = %q, want %q", got, want)
	}
}
