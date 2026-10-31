// HYDRA-UMC-JOB-DISPATCHER - production events
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// When a robot reports that a job ended, the dispatcher knows how long the cycle took and whether it
// went well. With the switch on it hands that over as a ProductionEvent of the shared contract of
// HYDRA-UMC-SDK (the production reports turn it into OEE). The switch is off by default and the
// events never change what the dispatcher does: a sink that fails is logged and nothing else.
package dispatcher

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"
)

// MaxCycleTimeSeconds is the longest cycle the contract accepts: one week.
const MaxCycleTimeSeconds = 604800

var productionIdentifier = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// ProductionEvent is one finished cycle of one robot, in the wire form of the contract.
type ProductionEvent struct {
	SchemaVersion string  `json:"schema_version"`
	SourceID      string  `json:"source_id"`
	AtUTC         string  `json:"at_utc"`
	Good          bool    `json:"good"`
	CycleTimeS    float64 `json:"cycle_time_s"`
	JobID         string  `json:"job_id,omitempty"`
	RejectReason  string  `json:"reject_reason,omitempty"`
}

// Validate applies the checks of the ProductionEvent contract.
func (p ProductionEvent) Validate() error {
	if p.SchemaVersion != "1.0" {
		return errors.New("schema_version must be '1.0'")
	}
	if !productionIdentifier.MatchString(p.SourceID) {
		return errors.New("source_id must be 1 to 64 letters, digits, dots, colons, dashes or underscores")
	}
	if p.JobID != "" && !productionIdentifier.MatchString(p.JobID) {
		return errors.New("job_id must be 1 to 64 letters, digits, dots, colons, dashes or underscores")
	}
	if _, err := time.Parse(time.RFC3339, p.AtUTC); err != nil {
		return errors.New("at_utc must be an RFC 3339 date-time")
	}
	if math.IsNaN(p.CycleTimeS) || math.IsInf(p.CycleTimeS, 0) || p.CycleTimeS <= 0 || p.CycleTimeS > MaxCycleTimeSeconds {
		return fmt.Errorf("cycle_time_s must be a number greater than 0 and at most %d", MaxCycleTimeSeconds)
	}
	if n := len([]rune(p.RejectReason)); p.RejectReason != "" && n > 120 {
		return errors.New("reject_reason must be a text of 1 to 120 characters")
	}
	return nil
}

// ProductionEventSink receives the events; an error is logged and never reaches the caller of CompleteJob.
type ProductionEventSink func(ProductionEvent) error

// SetProductionEvents switches the events on with the sink that receives them (nil switches them off, the default).
func (e *Engine) SetProductionEvents(sink ProductionEventSink) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.productionSink = sink
}

// ProductionEventsEnabled reports whether a sink is set.
func (e *Engine) ProductionEventsEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.productionSink != nil
}

// productionEventLocked builds the event of a job that has just ended; nil when the events are off or the cycle cannot be described by the contract
// (never assigned, an end before the start, longer than a week, an id outside the contract): such a job is logged and counted as skipped, never invented.
func (e *Engine) productionEventLocked(j *Job) (ProductionEventSink, *ProductionEvent) {
	if e.productionSink == nil {
		return nil, nil
	}
	event := ProductionEvent{
		SchemaVersion: "1.0",
		SourceID:      j.AssignedRobot,
		AtUTC:         time.UnixMilli(j.CompletedAtMs).UTC().Format(time.RFC3339Nano),
		Good:          j.Status == StatusDone,
		CycleTimeS:    float64(j.CompletedAtMs-j.AssignedAtMs) / 1000,
		JobID:         j.ID,
	}
	if j.AssignedAtMs == 0 {
		log.Printf("[job-dispatcher] production event of job %q skipped: it has no start time", j.ID)
		return nil, nil
	}
	if err := event.Validate(); err != nil {
		log.Printf("[job-dispatcher] production event of job %q skipped: %v", j.ID, err)
		return nil, nil
	}
	return e.productionSink, &event
}

func deliver(sink ProductionEventSink, event ProductionEvent) {
	if err := sink(event); err != nil {
		log.Printf("[job-dispatcher] production event of job %q not delivered: %v", event.JobID, err)
	}
}

// JSONLinesSink appends every event as one JSON line to the file (created when missing).
func JSONLinesSink(path string) ProductionEventSink {
	var mu sync.Mutex
	return func(event ProductionEvent) error {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = file.Write(append(line, '\n'))
		return err
	}
}

// HTTPSink posts every event as JSON to the URL (with a bearer token when one is given); anything but a 2xx answer is an error.
func HTTPSink(url, token string, timeout time.Duration) ProductionEventSink {
	client := &http.Client{Timeout: timeout}
	return func(event ProductionEvent) error {
		body, err := json.Marshal(event)
		if err != nil {
			return err
		}
		request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return fmt.Errorf("the receiver answered %d", response.StatusCode)
		}
		return nil
	}
}

// AsyncSink delivers on its own goroutine through a queue of the given size, so that a slow receiver never holds up a completion; an event that finds the queue
// full is dropped and logged. The returned function stops it after the queue has been emptied.
func AsyncSink(sink ProductionEventSink, queue int) (ProductionEventSink, func()) {
	if queue < 1 {
		queue = 1
	}
	events := make(chan ProductionEvent, queue)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range events {
			deliver(sink, event)
		}
	}()
	var once sync.Once
	var closed sync.RWMutex
	stopped := false
	enqueue := func(event ProductionEvent) error {
		closed.RLock()
		defer closed.RUnlock()
		if stopped {
			return errors.New("the sink is closed")
		}
		select {
		case events <- event:
			return nil
		default:
			return errors.New("the queue of production events is full: the event was dropped")
		}
	}
	stop := func() {
		once.Do(func() {
			closed.Lock()
			stopped = true
			close(events)
			closed.Unlock()
		})
		<-done
	}
	return enqueue, stop
}
