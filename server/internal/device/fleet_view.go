package device

import (
	"sort"
	"strings"
	"time"

	"github.com/zetesis-labs/sebastian/server/internal/discovery"
)

// Functional core of the fleet view: no I/O, no clock, no store. The Service
// (service side of this package) feeds it rows, announces and the time.

// ── fleet view ──────────────────────────────────────────────────────────────

// deriveState joins one inventory row (nil when the unit only exists on the
// LAN) with its announce (nil when not seen) into the state the operator sees.
// leaving: the unit was forgotten here moments ago (its announce is stale).
//
// Transitions are explicit (spec §3.2): an adoption puts the unit in
// "joining" until its first poll; a unit taken by another control room is
// "moved" for its old owner as soon as the LAN says so; a forgotten unit is
// "leaving" until it announces itself free.
func deriveState(row *Device, seen *discovery.Seen, self string, leaving bool, now time.Time) State {
	if seen != nil {
		bound := strings.TrimRight(seen.ControlRoom, "/")
		if row != nil && row.Adopted && bound != self {
			// The announce predates our last contact: it is the stale one made
			// under the previous control room, and the poll is the unit's word.
			if row.ProfileReportedAt.After(seen.SeenAt) {
				return adoptedOrJoining(row, now)
			}
			return StateMoved
		}
		switch {
		case bound == "":
			return StateUnadopted
		case self != "" && bound == self:
			if row == nil {
				if leaving {
					return StateLeaving
				}
				return StateJoining // announces us, has not polled yet
			}
			if row.Adopted {
				return adoptedOrJoining(row, now)
			}
			return StateRegistered
		default:
			if failing(seen.LastError) {
				return StateOrphan
			}
			return StateManagedElsewhere
		}
	}
	if row == nil {
		return StateRegistered
	}
	if row.Adopted {
		if !row.ProfileReportedAt.IsZero() && now.Sub(row.ProfileReportedAt) <= absentAfter {
			return adoptedOrJoining(row, now)
		}
		if row.AdoptedAt.After(row.ProfileReportedAt) && now.Sub(row.AdoptedAt) <= joiningFor {
			return StateJoining
		}
		return StateAbsent
	}
	return StateRegistered
}

// adoptedOrJoining: adopted here; "joining" while it has not contacted us
// since the adoption (rebooting), for at most joiningFor.
func adoptedOrJoining(row *Device, now time.Time) State {
	if row.AdoptedAt.After(row.ProfileReportedAt) && now.Sub(row.AdoptedAt) <= joiningFor {
		return StateJoining
	}
	return StateAdopted
}

// failing: the unit says its last contact with its control room did not work.
// "adopt-denied:<ip>" is informational, not a failing control room.
func failing(lastError string) bool {
	return lastError != "" && lastError != "ok" && !strings.HasPrefix(lastError, "adopt-denied")
}

// fleetView is the pure join of the inventory rows with the LAN announces:
// every unit gets its state, LAN facts fill what the row lacks, and the list
// is ordered ours-first (RF-10).
func fleetView(rows []Device, seen []discovery.Seen, self string, leaving map[string]bool, now time.Time) []Device {
	byID := make(map[string]*Device, len(rows))
	out := make([]Device, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	for _, sn := range seen {
		d, ok := byID[sn.ID]
		if !ok {
			out = append(out, Device{ID: sn.ID, DisplayName: sn.ID, Enabled: true})
			d = &out[len(out)-1]
			byID[sn.ID] = d
		}
		sn := sn
		d.IP, d.Firmware, d.ControlRoom, d.LastError, d.SeenOnLanAt = sn.IP, sn.Firmware, sn.ControlRoom, sn.LastError, sn.SeenAt
		if d.ReportedConfig == "" {
			d.ReportedConfig = sn.ConfigVersion
		}
		if d.ReportedProfile == "" {
			d.ReportedProfile = sn.Profile
		}
	}
	seenByID := map[string]*discovery.Seen{}
	for i := range seen {
		seenByID[seen[i].ID] = &seen[i]
	}
	for i := range out {
		var row *Device
		if _, inDB := rowIDs(rows)[out[i].ID]; inDB {
			row = &out[i]
		}
		out[i].State = deriveState(row, seenByID[out[i].ID], self, leaving[out[i].ID], now)
		if out[i].Firmware == "" {
			out[i].Firmware = out[i].ReportedFirmware
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := stateRank(out[i].State), stateRank(out[j].State)
		if ri != rj {
			return ri < rj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func rowIDs(rows []Device) map[string]struct{} {
	ids := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		ids[r.ID] = struct{}{}
	}
	return ids
}

// Order of the list (RF-10): ours first, then what needs attention.
func stateRank(st State) int {
	switch st {
	case StateAdopted, StateJoining, StateMoved:
		return 0
	case StateOrphan, StateLeaving:
		return 1
	case StateUnadopted:
		return 2
	case StateRegistered:
		return 3
	case StateManagedElsewhere:
		return 4
	default:
		return 5
	}
}
