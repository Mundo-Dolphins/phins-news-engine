package dictionary

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// ContentHash returns a deterministic hex-encoded SHA-256 hash over the
// dictionary's substantive content (categories, entries, decision
// thresholds). Bookkeeping fields that change on every write (Version,
// GeneratedAt, Checksum, Sources, SnapshotRef, ChangeCount, Tool, Name,
// Language) are excluded so the hash reflects content, not metadata.
//
// Entry.UpdatedAt/ActiveSince/ActiveUntil are truncated to whole UTC days
// before hashing. dictionaryfile round-trips these through a
// "2006-01-02"-only YAML representation, so any sub-day precision a caller
// stamped (e.g. clk.Now()) is silently lost the next time the file is
// loaded; hashing the untruncated value would stamp a checksum that no
// longer matches its own content as soon as the file is reloaded.
func ContentHash(d Dictionary) string {
	cats := make([]Category, len(d.Categories))
	copy(cats, d.Categories)
	sort.Slice(cats, func(i, j int) bool { return cats[i].Name < cats[j].Name })
	for i := range cats {
		entries := make([]Entry, len(cats[i].Entries))
		copy(entries, cats[i].Entries)
		for j := range entries {
			entries[j].UpdatedAt = truncateToUTCDate(entries[j].UpdatedAt)
			entries[j].ActiveSince = truncatePtrToUTCDate(entries[j].ActiveSince)
			entries[j].ActiveUntil = truncatePtrToUTCDate(entries[j].ActiveUntil)
		}
		sort.Slice(entries, func(a, b int) bool { return entries[a].CanonicalName < entries[b].CanonicalName })
		cats[i].Entries = entries
	}

	payload := struct {
		Categories []Category
		Decision   Decision
	}{Categories: cats, Decision: d.Decision}

	b, err := json.Marshal(payload)
	if err != nil {
		// Category/Entry/Decision contain only primitives, strings,
		// slices, and time.Time — nothing json.Marshal can fail on.
		// A panic here means a future field addition broke that
		// invariant and needs immediate attention, not a silent hash.
		panic("dictionary: content hash marshal: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// truncateToUTCDate mirrors dictionaryfile's Format("2006-01-02")/
// Parse("2006-01-02", ...) round trip: Format renders the year/month/day
// in t's own location (no UTC conversion), and Parse reconstructs midnight
// UTC of that same year/month/day. Using t.Date() here (not t.UTC()) keeps
// the two in agreement — converting to UTC first would shift the
// calendar day for any non-UTC t whose local date differs from its UTC
// date (e.g. late-night wall-clock times), producing a hash that once
// again wouldn't survive a file round trip.
func truncateToUTCDate(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func truncatePtrToUTCDate(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := truncateToUTCDate(*t)
	return &d
}
