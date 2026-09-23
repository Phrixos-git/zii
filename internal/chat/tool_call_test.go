package chat

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestFunctionCallNormalizedArguments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{name: "object", input: `{"a":1}`, valid: true},
		{name: "encoded object string", input: `"{\"a\":1}"`, valid: true},
		{name: "empty", input: ""},
		{name: "null", input: `null`},
		{name: "array", input: `[]`},
		{name: "number", input: `1`},
		{name: "encoded scalar", input: `"1"`},
		{name: "malformed", input: `{bad}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := json.RawMessage(tt.input)
			before := append(json.RawMessage(nil), input...)
			got, err := (FunctionCall{Arguments: input}).NormalizedArguments()
			if !tt.valid {
				if err == nil {
					t.Fatalf("NormalizedArguments(%q) succeeded, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizedArguments(%q): %v", tt.input, err)
			}
			if !bytes.Equal(input, before) {
				t.Fatalf("NormalizedArguments mutated input: got %q, want %q", input, before)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(got, &object); err != nil || object == nil {
				t.Fatalf("normalized arguments %q are not a JSON object (object=%v, err=%v)", got, object, err)
			}
		})
	}
}
