package api

import (
	"github.com/DnzzL/herdr-automations/internal/history"
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
		return nil, Fail(CodeNotFound, "no run %q in the history", runID)
	}
	res := &RunResult{Run: runOf(recs[len(recs)-1]), Records: []Record{}}
	for _, r := range recs {
		res.Records = append(res.Records, recordOf(r))
	}
	return res, nil
}
