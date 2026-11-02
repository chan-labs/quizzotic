// SPDX-License-Identifier: Apache-2.0

package tags_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/chan-labs/quizzotic/internal/platform"
	"github.com/chan-labs/quizzotic/internal/tags"
)

func TestAppliedOperationKeepsItsResult(t *testing.T) {
	f := newSplitFx(t)
	f.tag("alpha")
	f.tag("beta")
	propose := func(slug, to string) tags.OpDetail {
		d, err := f.svc.Propose(f.ctx, f.owner, f.agent(), tags.ProposeInput{
			Kind: tags.OpRename, Payload: json.RawMessage(`{"tag":"` + slug + `","slug":"` + to + `"}`)})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a, b := propose("alpha", "alpha2"), propose("beta", "beta2")
	applied, err := f.svc.ApplyOperation(f.ctx, f.owner, f.actor, a.Operation.ID)
	if err != nil || applied.Result == nil {
		t.Fatalf("apply = %+v, %v", applied, err)
	}
	if _, err := f.svc.RejectOperation(f.ctx, f.owner, f.actor, b.Operation.ID); err != nil {
		t.Fatal(err)
	}

	got, err := f.svc.GetOperation(f.ctx, f.owner, a.Operation.ID)
	if err != nil || !reflect.DeepEqual(got.Result, applied.Result) || got.Preview != nil {
		t.Fatalf("get applied = %+v (%v), want result %+v", got, err, applied.Result)
	}
	list, _, err := f.svc.ListOperationDetails(f.ctx, f.owner, "", platform.Page{Limit: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v (%v)", list, err)
	}
	if !reflect.DeepEqual(list[0].Result, applied.Result) || list[1].Operation.Status != tags.OpRejected || list[1].Result != nil {
		t.Fatalf("list details = %+v", list)
	}
}
