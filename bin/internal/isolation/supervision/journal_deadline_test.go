package supervision

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type canceledRecovery struct {
	stage                string
	cancel               context.CancelFunc
	inspections, cleanup int
}

func (r *canceledRecovery) Inspect(context.Context, Effect) (IdentityState, error) {
	r.inspections++
	if r.stage == "inspect_absent" {
		r.cancel()
		return IdentityAbsent, nil
	}
	if r.stage == "inspect_owned" {
		r.cancel()
	}
	if r.inspections == 2 {
		r.cancel()
		return IdentityAbsent, nil
	}
	return IdentityOwned, nil
}

func (r *canceledRecovery) Cleanup(context.Context, Effect) error {
	r.cleanup++
	if r.stage == "cleanup" {
		r.cancel()
	}
	return nil
}

func TestRecoveryRejectsLateNativeCompletion(t *testing.T) {
	for _, stage := range []string{"inspect_absent", "inspect_owned", "cleanup", "inspect_after_cleanup"} {
		t.Run(stage, func(t *testing.T) {
			journal, err := OpenJournal(filepath.Join(t.TempDir(), "recovery.json"), 1)
			if err != nil {
				t.Fatal(err)
			}
			effect := testEffect("late")
			effect.State, effect.Identity = EffectOwned, "native-owned"
			if err := journal.Put(effect); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reconciler := &canceledRecovery{stage: stage, cancel: cancel}
			result, err := journal.Recover(ctx, reconciler, time.Second)
			if !errors.Is(err, context.Canceled) || len(result.Resolved) != 0 || len(result.Remaining) != 1 || result.Remaining[0].State != EffectUncertain {
				t.Fatalf("late completion: result=%+v err=%v", result, err)
			}
			wantInspections, wantCleanup := 1, 0
			if stage == "cleanup" || stage == "inspect_after_cleanup" {
				wantCleanup = 1
			}
			if stage == "inspect_after_cleanup" {
				wantInspections = 2
			}
			if reconciler.inspections != wantInspections || reconciler.cleanup != wantCleanup {
				t.Fatalf("work continued after cancellation: inspections=%d cleanup=%d", reconciler.inspections, reconciler.cleanup)
			}
		})
	}
}
