package httpwire_test

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/cplieger/go/httpwire"
)

// TestPolicyOfIsTheDocumentsPolicy holds PolicyOf to the x-idempotency the
// keyed fixture's committed document carries on every row, which New
// accepts, so a writer of the document and New read one policy.
func TestPolicyOfIsTheDocumentsPolicy(t *testing.T) {
	b, err := os.ReadFile("testdata/openapi/keyed.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Policy      map[string]any `json:"x-idempotency"`
			OperationID string         `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	published := map[string]map[string]any{}
	for _, ops := range doc.Paths {
		for _, op := range ops {
			published[op.OperationID] = op.Policy
		}
	}
	d, err := httpwire.Derive(keyedTable(t), guardCodes)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Endpoints) != len(published) {
		t.Fatalf("%d rows, %d operations", len(d.Endpoints), len(published))
	}
	for i := range d.Endpoints {
		ep := &d.Endpoints[i]
		t.Run(ep.Name, func(t *testing.T) {
			p, ok := httpwire.PolicyOf(ep)
			want := published[ep.Name]
			if ok != (want != nil) || ok != ep.Keyed {
				t.Fatalf("PolicyOf ok %v; Keyed %v, the document's x-idempotency %v", ok, ep.Keyed, want)
			}
			if !ok {
				return
			}
			if got := policyJSON(t, p); !reflect.DeepEqual(got, want) {
				t.Errorf("PolicyOf = %v, the document's x-idempotency %v", got, want)
			}
		})
	}
}

func TestPolicyOfMembers(t *testing.T) {
	ep := httpwire.Endpoint{Keyed: true, Window: 90*time.Second + 999*time.Millisecond, MaxKept: 5, Result: "jobs.read"}
	p, ok := httpwire.PolicyOf(&ep)
	want := httpwire.IdempotencyPolicy{
		WindowS: 90, Scope: "credential,operation", Concurrent: "join", Kept: "success_and_effectful_refusals",
		MaxKept: 5, Result: "jobs.read",
	}
	if !ok || p != want {
		t.Errorf("PolicyOf = %+v, %v; want %+v", p, ok, want)
	}
	got, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `{"window_s":90,"scope":"credential,operation","concurrent":"join","kept":"success_and_effectful_refusals","max_kept":5,"result":"jobs.read"}`
	if !jsonSame(t, got, []byte(wantJSON)) {
		t.Errorf("as JSON %s, want %s", got, wantJSON)
	}
	if p, ok := httpwire.PolicyOf(&httpwire.Endpoint{Window: time.Minute}); ok || p != (httpwire.IdempotencyPolicy{}) {
		t.Errorf("PolicyOf of a row that is not Keyed = %+v, %v; want the zero policy, false", p, ok)
	}
}

// policyJSON answers p as the decoded JSON value a document holds.
func policyJSON(t *testing.T, p httpwire.IdempotencyPolicy) map[string]any {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
