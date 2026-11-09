package dictionarywiring

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
	"github.com/Mundo-Dolphins/phins-news-engine/domain/dictionary"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryfile"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionaryhistory"
	"github.com/Mundo-Dolphins/phins-news-engine/infra/dictionarysnapshot"
)

// DiffOptions selects what Diff compares. From/To compare recorded history
// versions; FromFile/ToFile compare two files; otherwise the dictionary at
// DictionaryPath is compared with SnapshotPath or the latest snapshot per
// source in SnapshotDir. The caller supplies DictionaryPath (normally
// cfg.Dictionary.Path); an empty SnapshotDir means dictionarysnapshot.DefaultDir.
type DiffOptions struct {
	DictionaryPath string
	SnapshotPath   string
	SnapshotDir    string
	FromFile       string
	ToFile         string
	From           string
	To             string
}

// Diff computes the dictionary change proposals for opts.
func Diff(opts DiffOptions, now time.Time) ([]dictionaryupdate.Proposal, error) {
	var current dictionary.Dictionary
	var observed []dictionaryupdate.SourceEntity

	switch {
	case opts.From != "":
		// Historical mode: diff a recorded version's on-disk artifacts
		// against either another recorded version (To) or the current
		// dictionary on disk (default). Reads only file artifacts.
		fromArtifacts, err := dictionaryhistory.ReadVersion(dictionaryhistory.DefaultDir, opts.From)
		if err != nil {
			return nil, err
		}
		current = fromArtifacts.Dictionary

		if opts.To != "" {
			toArtifacts, err := dictionaryhistory.ReadVersion(dictionaryhistory.DefaultDir, opts.To)
			if err != nil {
				return nil, err
			}
			observed = SourceEntitiesFromDictionary(toArtifacts.Dictionary)
		} else {
			currentOnDisk, err := dictionaryfile.Load(opts.DictionaryPath)
			if err != nil {
				return nil, err
			}
			observed = SourceEntitiesFromDictionary(currentOnDisk)
		}

	case opts.To != "":
		// To only makes sense alongside From (historical mode); on its own
		// it would be silently ignored, so reject it.
		return nil, fmt.Errorf("--to requires --from")

	case opts.FromFile != "" || opts.ToFile != "":
		if opts.FromFile == "" || opts.ToFile == "" {
			return nil, fmt.Errorf("--from-file and --to-file must be given together")
		}
		fromDict, err := dictionaryfile.Load(opts.FromFile)
		if err != nil {
			return nil, err
		}
		current = fromDict
		toDict, err := dictionaryfile.Load(opts.ToFile)
		if err != nil {
			return nil, err
		}
		observed = SourceEntitiesFromDictionary(toDict)

	default:
		loaded, err := dictionaryfile.Load(opts.DictionaryPath)
		if err != nil {
			return nil, err
		}
		current = loaded

		if opts.SnapshotPath != "" {
			snap, err := dictionarysnapshot.Read(opts.SnapshotPath)
			if err != nil {
				return nil, err
			}
			observed = snap.Entities
		} else {
			dir := opts.SnapshotDir
			if dir == "" {
				dir = dictionarysnapshot.DefaultDir
			}
			paths, err := latestSnapshotPathsBySource(dir)
			if err != nil {
				return nil, err
			}
			// Iterate sources in a fixed order so the observed slice (and
			// therefore Compare's output for same-priority ties) is
			// deterministic across runs.
			sources := make([]dictionaryupdate.SourceKind, 0, len(paths))
			for source := range paths {
				sources = append(sources, source)
			}
			sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
			for _, source := range sources {
				snap, err := dictionarysnapshot.Read(paths[source])
				if err != nil {
					return nil, err
				}
				observed = append(observed, snap.Entities...)
			}
		}
	}

	policy := dictionaryupdate.DeactivationPolicy{MinimumSuccessfulAbsences: 2, MinimumElapsedDays: 3}
	return dictionaryupdate.Compare(current, observed, dictionaryupdate.AbsenceLog{}, policy, now), nil
}

// latestSnapshotPathsBySource returns the most recent snapshot file per
// source kind found in dir. Snapshot filenames are fetched-<source>-
// <YYYYMMDD-HHMMSS>.json; since none of the three SourceKind values
// contain a hyphen, splitting on the first hyphen after stripping the
// fetched-/.json wrapper reliably separates source from timestamp, and
// lexicographic sort within a single source's filename group still
// equals chronological order (only the timestamp suffix varies).
//
// The host's dictionary fetcher writes one file per source per run, so a snapshot
// directory normally holds several current files at once (one per
// source) rather than a single "latest" file across all sources — the
// diff's default mode must merge each source's own latest snapshot,
// not pick whichever single file happens to sort last overall.
func latestSnapshotPathsBySource(dir string) (map[dictionaryupdate.SourceKind]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read snapshot dir %s: %w", dir, err)
	}
	bySource := make(map[dictionaryupdate.SourceKind][]string)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "fetched-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		trimmed := strings.TrimSuffix(strings.TrimPrefix(name, "fetched-"), ".json")
		parts := strings.SplitN(trimmed, "-", 2)
		if len(parts) != 2 {
			continue
		}
		source := dictionaryupdate.SourceKind(parts[0])
		bySource[source] = append(bySource[source], name)
	}
	if len(bySource) == 0 {
		return nil, fmt.Errorf("no snapshot files found in %s", dir)
	}
	latest := make(map[dictionaryupdate.SourceKind]string, len(bySource))
	for source, names := range bySource {
		sort.Strings(names)
		latest[source] = filepath.Join(dir, names[len(names)-1])
	}
	return latest, nil
}

// SourceEntitiesFromDictionary converts the roster, coaching staff and front
// office categories of d into source entities, the shape Compare observes.
func SourceEntitiesFromDictionary(d dictionary.Dictionary) []dictionaryupdate.SourceEntity {
	kindForCategory := map[string]dictionaryupdate.SourceKind{
		"active_roster_core": dictionaryupdate.SourceKindRoster,
		"coaching_staff":     dictionaryupdate.SourceKindCoaches,
		"front_office":       dictionaryupdate.SourceKindFrontOffice,
	}
	var out []dictionaryupdate.SourceEntity
	for _, cat := range d.Categories {
		kind, ok := kindForCategory[cat.Name]
		if !ok {
			continue
		}
		for _, e := range cat.Entries {
			out = append(out, dictionaryupdate.SourceEntity{
				DisplayName:  e.CanonicalName,
				CanonicalKey: dictionaryupdate.CanonicalKey(e.CanonicalName),
				Kind:         kind,
				Role:         e.Role,
				Position:     e.Position,
				Number:       e.Number,
				RosterStatus: e.RosterStatus,
			})
		}
	}
	return out
}
