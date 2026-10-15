package dictionary

import (
	"fmt"
	"strconv"
	"strings"
)

// BumpPatchVersion increments the patch component of a semver-shaped
// "MAJOR.MINOR.PATCH" version string — used by `dictionary update` when
// writing an approved change back to configs/dictionary.yml.
// Unlike v1's dictionary tooling (which treated its version field as a
// plain integer and incremented it, which would corrupt a real semver
// string), a malformed version here fails loudly rather than silently
// producing garbage.
func BumpPatchVersion(version string) (string, error) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("dictionary: version %q is not MAJOR.MINOR.PATCH semver", version)
	}
	if _, err := strconv.Atoi(parts[0]); err != nil {
		return "", fmt.Errorf("dictionary: version %q has a non-numeric major component: %w", version, err)
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return "", fmt.Errorf("dictionary: version %q has a non-numeric minor component: %w", version, err)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", fmt.Errorf("dictionary: version %q has a non-numeric patch component: %w", version, err)
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch+1), nil
}
