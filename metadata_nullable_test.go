package devin

import (
	"encoding/json"
	"testing"
)

func TestNullableMetadataValuesSurviveUpdateRequestRoundTrips(t *testing.T) {
	t.Run("automation", func(t *testing.T) {
		var request AutomationUpdateRequest
		if err := json.Unmarshal([]byte(`{"metadata":{"remove":null,"keep":"value"}}`), &request); err != nil {
			t.Fatal(err)
		}
		assertNullableMetadataRoundTrip(t, request)
	})
	t.Run("oncall responder", func(t *testing.T) {
		var request OncallResponderUpdateRequest
		if err := json.Unmarshal([]byte(`{"metadata":{"remove":null,"keep":"value"}}`), &request); err != nil {
			t.Fatal(err)
		}
		assertNullableMetadataRoundTrip(t, request)
	})
}

func assertNullableMetadataRoundTrip(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := string(decoded["metadata"]["remove"]); got != "null" {
		t.Fatalf("metadata.remove after round trip = %s, want null (wire JSON %s)", got, encoded)
	}
	if got := string(decoded["metadata"]["keep"]); got != `"value"` {
		t.Fatalf("metadata.keep after round trip = %s, want \"value\"", got)
	}
}
