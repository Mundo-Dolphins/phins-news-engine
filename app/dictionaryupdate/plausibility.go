package dictionaryupdate

import "fmt"

type PlausibilityBounds struct {
	MinimumEntities     int
	MaximumEntities     int
	MaximumMissingRatio float64
}

// CheckPlausibility flags an implausible fetch result — too few entities,
// an implausibly large jump, or (when a previous count is available) a
// drop beyond MaximumMissingRatio. A non-nil error means the result must
// not be used to propose deactivations and requires reinforced
// confirmation before the workflow proceeds.
func CheckPlausibility(bounds PlausibilityBounds, observedCount, previousCount int) error {
	if observedCount < bounds.MinimumEntities {
		return fmt.Errorf("dictionaryupdate: implausible result: got %d entities, minimum expected is %d", observedCount, bounds.MinimumEntities)
	}
	if bounds.MaximumEntities > 0 && observedCount > bounds.MaximumEntities {
		return fmt.Errorf("dictionaryupdate: implausible result: got %d entities, maximum expected is %d", observedCount, bounds.MaximumEntities)
	}
	if previousCount > 0 && bounds.MaximumMissingRatio > 0 {
		missingRatio := float64(previousCount-observedCount) / float64(previousCount)
		if missingRatio > bounds.MaximumMissingRatio {
			return fmt.Errorf("dictionaryupdate: implausible result: entity count dropped %.0f%% (from %d to %d), exceeds maximum_missing_ratio %.0f%%",
				missingRatio*100, previousCount, observedCount, bounds.MaximumMissingRatio*100)
		}
	}
	return nil
}
