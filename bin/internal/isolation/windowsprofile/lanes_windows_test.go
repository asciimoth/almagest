//go:build windows

package windowsprofile

import (
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/roleentry"
	"golang.org/x/sys/windows"
)

func TestRequestLanesRejectsAuthorityShifts(t *testing.T) {
	h := windows.Handle(1)
	valid := Request{Plan: DefaultPlan(Runtime), Pipes: []windows.Handle{h, h + 1, h + 2, h + 3},
		Lanes: []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneData, roleentry.LaneDiagnostics, roleentry.LaneConfig}}
	lanes, err := requestLanes(valid)
	if err != nil || len(lanes) != len(valid.Pipes) || lanes[3] != roleentry.LaneConfig {
		t.Fatalf("valid lanes = %v, %v", lanes, err)
	}
	cases := []Request{
		{Plan: valid.Plan, Pipes: valid.Pipes, Lanes: valid.Lanes[:3]},
		{Plan: valid.Plan, Pipes: valid.Pipes[:3], Lanes: []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneData, roleentry.LaneData}},
		{Plan: valid.Plan, Pipes: valid.Pipes[:2], Lanes: []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneData}},
		{Plan: DefaultPlan(Writer), Pipes: valid.Pipes[:2], Lanes: []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneConfig}},
		{Plan: DefaultPlan(Command), Pipes: valid.Pipes[:2], Lanes: []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneAdmin}},
	}
	for index, request := range cases {
		if lanes, err := requestLanes(request); err == nil {
			t.Fatalf("case %d accepted lanes %v", index, lanes)
		}
	}
}

func FuzzRequestLanes(f *testing.F) {
	f.Add(byte(0), byte(0), byte(0))
	f.Add(byte(1), byte(2), byte(3))
	all := []roleentry.Lane{roleentry.LaneBootstrap, roleentry.LaneData, roleentry.LaneDiagnostics, roleentry.LaneAdmin,
		roleentry.LaneConfig, roleentry.LaneCommand, roleentry.LaneCallback, roleentry.Lane("unknown")}
	f.Fuzz(func(t *testing.T, a, b, c byte) {
		request := Request{Plan: DefaultPlan(Runtime), Pipes: []windows.Handle{1, 2, 3},
			Lanes: []roleentry.Lane{all[int(a)%len(all)], all[int(b)%len(all)], all[int(c)%len(all)]}}
		lanes, err := requestLanes(request)
		if err == nil {
			seen := make(map[roleentry.Lane]bool, len(lanes))
			for index, lane := range lanes {
				if lane != request.Lanes[index] || seen[lane] {
					t.Fatalf("accepted unsafe lane map: %v", lanes)
				}
				seen[lane] = true
			}
			if len(lanes) != 3 || lanes[0] != roleentry.LaneBootstrap || !seen[roleentry.LaneData] || !seen[roleentry.LaneDiagnostics] {
				t.Fatalf("accepted unsafe lane map: %v", lanes)
			}
		}
	})
}
