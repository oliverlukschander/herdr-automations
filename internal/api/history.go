package api

import (
	"fmt"
	"time"

	"github.com/DnzzL/herdr-automations/internal/history"
	"github.com/DnzzL/herdr-automations/internal/request"
)

// HistoryResult is `history --json [name] [--limit N]`.
type HistoryResult struct {
	Head
	Runs []Run `json:"runs"`
}

// RunResult is `history --json --run ID`: one run and every record of it.
type RunResult struct {
	Head
	Run     Run      `json:"run"`
	Records []Record `json:"records"`
}

// History answers `history --json`. name may be empty for every automation.
func History(name string, limit int) (*HistoryResult, error) {
	runs, err := history.Runs(name, limit)
	if err != nil {
		return nil, err
	}
	res := &HistoryResult{Runs: []Run{}}
	for _, r := range runs {
		res.Runs = append(res.Runs, runOf(r))
	}
	return res, nil
}

// RunTimeline answers `history --json --run ID`.
func RunTimeline(runID string) (*RunResult, error) {
	recs, err := history.Timeline(runID)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		// Asked for with run --detach and not yet taken by the daemon. Past
		// request.MaxWait no daemon will run it any more: it is what the
		// daemon would record it as, missed.
		if q := request.Find(runID); q != nil {
			started, _ := history.StartedAt(runID)
			manual := string(history.TriggerManual)
			run := Run{
				RunID: runID, Automation: q.Automation, Status: "requested", Trigger: &manual,
				StartedAt: Time{started}, UpdatedAt: Time{q.RequestedAt},
			}
			if now := time.Now(); q.Stale(now) {
				run.Status, run.UpdatedAt = string(history.StatusMissed), Time{now}
				run.Error = str(fmt.Sprintf("no scheduler took the run-now request within %s", request.MaxWait))
			}
			return &RunResult{Records: []Record{}, Run: run}, nil
		}
		return nil, Fail(CodeNotFound, "no run %q in the history", runID)
	}
	res := &RunResult{Run: runOf(recs[len(recs)-1]), Records: []Record{}}
	for _, r := range recs {
		res.Records = append(res.Records, recordOf(r))
	}
	return res, nil
}
