package metabft

import (
	"errors"
	"testing"
)

func TestEvidenceVerify(t *testing.T) {
	net := newTestNet(t, 4)
	a := net.signed(t, 1, MsgPrepare, 10, 0, digestA)
	b := net.signed(t, 1, MsgPrepare, 10, 0, digestB)

	if idx, err := (&Evidence{*a, *b}).Verify(testChainID, net.set); err != nil || idx != 1 {
		t.Fatalf("equivocating PREPAREs: %d, %v", idx, err)
	}
	commitA := net.signed(t, 2, MsgCommit, 10, 0, digestA)
	commitB := net.signed(t, 2, MsgCommit, 10, 0, digestB)
	if _, err := (&Evidence{*commitA, *commitB}).Verify(testChainID, net.set); err != nil {
		t.Errorf("equivocating COMMITs: %v", err)
	}
	rc1 := net.signed(t, 3, MsgRoundChange, 10, 1, digestA)
	rc2 := &Message{Type: MsgRoundChange, Height: 10, Round: 1, ChainID: testChainID, Digest: digestA, Payload: []byte{1}}
	rc2.Sign(net.keys[3])
	if _, err := (&Evidence{*rc1, *rc2}).Verify(testChainID, net.set); err != nil {
		t.Errorf("two ROUND-CHANGEs for one round: %v", err)
	}

	for name, ev := range map[string]*Evidence{
		"same digest":       {*a, *net.signed(t, 1, MsgPrepare, 10, 0, digestA)},
		"different signers": {*a, *net.signed(t, 2, MsgPrepare, 10, 0, digestB)},
		"different rounds":  {*a, *net.signed(t, 1, MsgPrepare, 10, 1, digestB)},
		"different heights": {*a, *net.signed(t, 1, MsgPrepare, 11, 0, digestB)},
		"different types":   {*a, *net.signed(t, 1, MsgCommit, 10, 0, digestB)},
		"identical RC":      {*rc1, *rc1},
	} {
		if _, err := ev.Verify(testChainID, net.set); !errors.Is(err, errNotEquivocation) {
			t.Errorf("%s: %v", name, err)
		}
	}
	forged := *b
	forged.Signature = a.Signature
	if _, err := (&Evidence{*a, forged}).Verify(testChainID, net.set); err == nil {
		t.Error("evidence with a forged message accepted")
	}
	if _, err := (&Evidence{*a, *b}).Verify(testChainID+1, net.set); err == nil {
		t.Error("evidence for another chain accepted")
	}
}

func TestEvidenceStore(t *testing.T) {
	net := newTestNet(t, 4)
	dir := t.TempDir()
	store, err := OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := net.signed(t, 1, MsgPrepare, 10, 0, digestA)
	b := net.signed(t, 1, MsgPrepare, 10, 0, digestB)
	c := net.signed(t, 2, MsgCommit, 9, 0, digestA)
	d := net.signed(t, 2, MsgCommit, 9, 0, digestB)

	for _, tt := range []struct {
		ev      *Evidence
		wantNew bool
	}{
		{&Evidence{*a, *b}, true},
		{&Evidence{*b, *a}, false}, // same pair, other order
		{&Evidence{*c, *d}, true},
	} {
		isNew, err := store.Add(tt.ev, testChainID, net.set)
		if err != nil || isNew != tt.wantNew {
			t.Errorf("Add: new %v, err %v; want new %v", isNew, err, tt.wantNew)
		}
	}
	if _, err := store.Add(&Evidence{*a, *a}, testChainID, net.set); err == nil {
		t.Error("stored a non-equivocation")
	}

	// Survives a restart, ordered by height.
	reopened, err := OpenEvidenceStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].First.Height != 9 || list[1].First.Height != 10 {
		t.Fatalf("listed %d pieces of evidence: %+v", len(list), list)
	}
	for _, ev := range list {
		if _, err := ev.Verify(testChainID, net.set); err != nil {
			t.Errorf("stored evidence no longer verifies: %v", err)
		}
	}
}
