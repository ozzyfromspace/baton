package state

import (
	"fmt"
	"time"
)

// OwnerTTL is how long an owner's heartbeat stays valid. The host refreshes it far more often; a host
// that died stops refreshing, and after OwnerTTL another host may take the project over.
const OwnerTTL = 30 * time.Second

// Owner is the baton host driving this project's plan. Only the owner's session acts on the plan: a
// second baton started in the same project runs as a plain passthrough.
type Owner struct {
	Instance  string    `json:"instance"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Heartbeat time.Time `json:"heartbeat"`
}

// Live reports whether the owner's heartbeat is fresh.
func (o *Owner) Live(now time.Time) bool {
	return o != nil && now.Sub(o.Heartbeat) < OwnerTTL
}

// Claim makes instance the owner unless another live owner holds the project.
func Claim(st *State, instance string, pid int, now time.Time) error {
	if o := st.Owner; o.Live(now) && o.Instance != instance {
		return fmt.Errorf("another baton session (pid %d, since %s) is driving this project", o.PID, o.Started.Local().Format("15:04"))
	}
	if st.Owner == nil || st.Owner.Instance != instance {
		st.Owner = &Owner{Instance: instance, PID: pid, Started: now}
	}
	st.Owner.Heartbeat = now
	return nil
}

// Release gives up ownership if instance still holds it.
func Release(st *State, instance string) {
	if st.Owner != nil && st.Owner.Instance == instance {
		st.Owner = nil
	}
}

// IsOwner reports whether instance owns the project. A stale heartbeat does not end ownership: it only
// lets another host take the project over (Claim). Until one does, the owner's own hooks keep working,
// for instance in the seconds after the machine wakes, before the host's next heartbeat.
func (st *State) IsOwner(instance string, now time.Time) bool {
	return instance != "" && st.Owner != nil && st.Owner.Instance == instance
}
