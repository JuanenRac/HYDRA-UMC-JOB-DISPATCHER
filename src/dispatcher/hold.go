// =============================================================================
// HYDRA-UMC-JOB-DISPATCHER - src/dispatcher/hold.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
// =============================================================================
//
// The global hold of assignments: while it is in force no job is given to a robot. Jobs are still accepted, reports of the robots are
// still taken and a job already on a robot goes on: the hold stops the queue, it does not stop a machine. Off unless the "hold" section
// of the configuration switches it on.

package dispatcher

// HoldStore is implemented by a Store that keeps the hold across a restart.
type HoldStore interface {
	SaveHold(held bool) error
	LoadHold() (bool, error)
}

// SetHoldEnabled switches the global hold on or off as a feature. On, the hold the store kept comes back; off, nothing is held.
func (e *Engine) SetHoldEnabled(on bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.holdEnabled, e.held = on, false
	if store, ok := e.store.(HoldStore); on && ok {
		held, err := store.LoadHold()
		if err != nil {
			return err
		}
		e.held = held
	}
	return nil
}

// HoldEnabled reports whether the hold exists at all.
func (e *Engine) HoldEnabled() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.holdEnabled
}

// Held reports whether assignments are held now.
func (e *Engine) Held() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.held
}

// SetHeld holds or releases the assignments. A hold that cannot be saved is applied anyway (and the error returned): the safe side. A
// release that cannot be saved is refused, so a restart can never come back holding something an operator was told was released, nor
// the other way round.
func (e *Engine) SetHeld(held bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var failed error
	if store, ok := e.store.(HoldStore); ok {
		failed = store.SaveHold(held)
		e.recordPersistOutcome(failed)
	}
	if failed != nil && !held {
		return failed
	}
	if e.held != held && e.events != nil {
		kind := EventReleased
		if held {
			kind = EventHeld
		}
		e.events.Publish(kind, "", "", 0)
	}
	e.held = held
	return failed
}
