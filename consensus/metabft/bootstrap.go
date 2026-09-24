package metabft

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// OpenNodeWAL applies the start-up rule of design §6.1 and returns the WAL
// together with the observerUntil value for NewCore.
//
// A PRE-PREPARE for height n is only accepted when n-1 is the local head, so
// a node whose head+1 is below bftBlock cannot have signed any PBFT message.
// Such a node creates its WAL and starts normally; that is how every
// validator gets one before the switch, rather than all of them waiting as
// observers at bftBlock and the chain stopping.
//
// Past that point a missing or corrupt WAL may have lost a vote at head+1,
// so the node signs nothing until head+1 is committed without it. A corrupt
// WAL is kept alongside for inspection.
func OpenNodeWAL(path string, head, bftBlock uint64) (*WAL, uint64, error) {
	w, _, err := OpenWAL(path, false)
	switch {
	case err == nil:
		return w, 0, nil
	case errors.Is(err, ErrWALMissing):
		w, _, err := OpenWAL(path, true)
		if err != nil {
			return nil, 0, err
		}
		if head+1 < bftBlock {
			return w, 0, nil
		}
		return w, head + 1, nil
	case errors.Is(err, ErrWALCorrupt):
		aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
		if err := os.Rename(path, aside); err != nil {
			return nil, 0, err
		}
		w, _, err := OpenWAL(path, true)
		if err != nil {
			return nil, 0, err
		}
		return w, head + 1, nil
	default:
		return nil, 0, err
	}
}
