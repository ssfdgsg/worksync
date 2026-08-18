package state

import (
	"fmt"
	"time"
)

// ErrIllegalTransition is returned when a transition is not allowed by the
// state machine of design §13.
type ErrIllegalTransition struct {
	From ContainerState
	To   ContainerState
	Op   string
}

func (e *ErrIllegalTransition) Error() string {
	return fmt.Sprintf("illegal transition %s -> %s (%s)", e.From, e.To, e.Op)
}

// Transition applies the design §13 state machine: it returns the resulting
// state or an *ErrIllegalTransition.
func Transition(from ContainerState, op string) (ContainerState, error) {
	type edge struct{ from, to ContainerState }
	transitions := map[string][]edge{
		"up":          {{StateAbsent, StateProvisioning}},
		"provision":   {{StateProvisioning, StateRunning}, {StateProvisioning, StateError}},
		"stop":        {{StateRunning, StateStopped}, {StateStopped, StateStopped}}, // idempotent §13.1
		"start":       {{StateStopped, StateRunning}, {StateRunning, StateRunning}}, // idempotent §13.1
		"commit":      {{StateRunning, StateCommitting}, {StateStopped, StateCommitting}},
		"commit-done": {{StateCommitting, StateRunning}, {StateCommitting, StateStopped}, {StateCommitting, StateError}},
		"rm":          {{StateRunning, StateRemoving}, {StateStopped, StateRemoving}},
		"remove-done": {{StateRemoving, StateAbsent}, {StateRemoving, StateError}},
		"recover":     {{StateError, StateRunning}, {StateError, StateStopped}},
	}
	for _, e := range transitions[op] {
		if e.from == from {
			return e.to, nil
		}
	}
	return from, &ErrIllegalTransition{From: from, To: from, Op: op}
}

// TransitionWithLock applies a transition and persists it. The caller must
// hold the project lock. When the container row does not exist yet, "up"
// seeds it into Provisioning.
func (d *DB) TransitionWithLock(projectID, op string) (ContainerState, error) {
	c, err := d.GetContainer(projectID)
	if err != nil {
		if err == ErrNotFound && op == "up" {
			return StateProvisioning, nil
		}
		return "", err
	}
	next, err := Transition(c.State, op)
	if err != nil {
		return "", err
	}
	c.State = next
	c.UpdatedAt = time.Now().UTC()
	if err := d.UpsertContainer(*c); err != nil {
		return "", err
	}
	return next, nil
}
