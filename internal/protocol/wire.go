package protocol

import (
	"encoding/json"
	"fmt"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/oplog"
)

// Version is the protocol version hello announces.
const Version = 1

// MaxLine is the longest request line accepted, newline excluded.
const MaxLine = 1 << 20

const (
	reqScan    = "scan"
	reqPlan    = "plan"
	reqExecute = "execute"
	reqCancel  = "cancel"
	reqTick    = "tick"
)

// Error codes, the `code` of an error event.
const (
	CodeBadRequest     = "bad_request"
	CodeLineTooLong    = "line_too_long"
	CodeUnknownRequest = "unknown_request"
	CodeBusy           = "busy"
	CodeUnknownScan    = "unknown_scan"
	CodeScanRunning    = "scan_running"
	CodeScanCanceled   = "scan_canceled"
	CodeScanSuperseded = "scan_superseded"
	CodeScanSpent      = "scan_spent"
	CodeUnmatched      = "unmatched"
	CodeTickFailed     = "tick_failed"
	CodeAutotrimLocked = "autotrim_locked"
	CodeUnknownPlan    = "unknown_plan"
	CodePlanExpired    = "plan_expired"
	CodeExecuteFailed  = "execute_failed"
)

type request struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	ScanID string `json:"scan_id"`
	// Select nil (absent or null) plans the default selection; an
	// empty list plans nothing.
	Select   *[]string `json:"select"`
	PlanID   string    `json:"plan_id"`
	Target   string    `json:"target"`
	Autotrim bool      `json:"autotrim"`
}

// head opens every event: its name and the id of the request it
// answers (empty for hello and for errors about an unparsed line).
type head struct {
	Event string `json:"event"`
	Re    string `json:"re,omitempty"`
}

type helloEvent struct {
	head
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	FDA      FDA    `json:"fda"`
}

// inlineEvent puts the fields of body, a JSON object, on the event
// line beside event and re.
type inlineEvent struct {
	head
	body any
}

func (e inlineEvent) MarshalJSON() ([]byte, error) {
	h, err := json.Marshal(e.head)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(e.body)
	if err != nil {
		return nil, err
	}
	if len(b) < 2 || b[0] != '{' {
		return nil, fmt.Errorf("%s event body is not a JSON object: %.40s", e.Event, b)
	}
	if string(b) == "{}" {
		return h, nil
	}
	return append(append(h[:len(h)-1], ','), b[1:]...), nil
}

type startEvent struct {
	head
	ScanID string `json:"scan_id"`
	Rules  int    `json:"rules"`
}

type findingEvent struct {
	head
	ScanID  string         `json:"scan_id"`
	Index   int            `json:"index"`
	TookMS  int64          `json:"took_ms"`
	Finding engine.Finding `json:"finding"`
}

type summaryEvent struct {
	head
	ScanID    string           `json:"scan_id"`
	Totals    engine.Totals    `json:"totals"`
	Exclusive map[string]int64 `json:"exclusive"`
}

type planEvent struct {
	head
	PlanID string        `json:"plan_id"`
	Plan   engine.Plan   `json:"plan"`
	Totals engine.Totals `json:"totals"`
}

type journalEvent struct {
	head
	Entry oplog.Entry `json:"entry"`
}

type doneEvent struct {
	head
}

type scanDoneEvent struct {
	head
	ElapsedMS int64 `json:"elapsed_ms"`
	Canceled  bool  `json:"canceled"`
}

type executeDoneEvent struct {
	head
	Result   executor.Result `json:"result"`
	Canceled bool            `json:"canceled"`
}

type errorEvent struct {
	head
	Code    string `json:"code"`
	Message string `json:"message"`
	// Unmatched lists the selectors of an unmatched plan request.
	Unmatched []string `json:"unmatched,omitempty"`
	// Result is what a failed execute did before it failed.
	Result *executor.Result `json:"result,omitempty"`
}

func done(re string) doneEvent { return doneEvent{head{"done", re}} }

func failure(re, code, message string) errorEvent {
	return errorEvent{head: head{"error", re}, Code: code, Message: message}
}
