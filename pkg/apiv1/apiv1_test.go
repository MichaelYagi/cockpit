package apiv1_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/api/contracttest"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// Every 200 example in the spec must decode into the matching Go type with
// no unknown fields, so hand-written types cannot drift from the spec (D6).
func TestSpecExamplesMatchTypes(t *testing.T) {
	spec := contracttest.Load(t)
	types := map[string]func() any{
		"getLivez":  func() any { return &apiv1.Livez{} },
		"getStatus": func() any { return &apiv1.Status{} },
	}
	checked := 0
	for _, item := range spec.Doc.Paths.Map() {
		for _, op := range item.Operations() {
			resp := op.Responses.Status(200)
			if resp == nil || resp.Value == nil {
				continue
			}
			mt := resp.Value.Content.Get("application/json")
			if mt == nil || mt.Example == nil {
				continue
			}
			newT, ok := types[op.OperationID]
			if !ok {
				t.Errorf("operation %s has an example but no Go type mapping in this test", op.OperationID)
				continue
			}
			raw, err := json.Marshal(mt.Example)
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(newT()); err != nil {
				t.Errorf("%s example does not match Go type: %v", op.OperationID, err)
			}
			checked++
		}
	}
	if checked != len(types) {
		t.Errorf("checked %d examples, expected %d", checked, len(types))
	}
}
