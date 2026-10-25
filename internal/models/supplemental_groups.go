package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
)

const maxSupplementalGroups = 16

// NormalizeSupplementalGroups returns host group IDs sorted ascending.
// An empty list returns nil. Group ID 0 is kept. Names, strings, negative
// IDs, non-integral numbers, duplicates, and more than 16 entries are rejected.
func NormalizeSupplementalGroups(raw any) ([]int64, error) {
	switch values := raw.(type) {
	case nil:
		return nil, nil
	case []int64:
		return normalizeGIDList(values)
	case []int:
		converted := make([]int64, len(values))
		for i, n := range values {
			converted[i] = int64(n)
		}
		return normalizeGIDList(converted)
	case []any:
		return normalizeAnyGIDs(values)
	default:
		return nil, errors.New("supplementalGroups must be a list of numeric host group IDs")
	}
}

func normalizeAnyGIDs(values []any) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	gids := make([]int64, 0, len(values))
	for _, value := range values {
		gid, err := supplementalGID(value)
		if err != nil {
			return nil, err
		}
		gids = append(gids, gid)
	}
	return normalizeGIDList(gids)
}

func normalizeGIDList(gids []int64) ([]int64, error) {
	if len(gids) == 0 {
		return nil, nil
	}
	if len(gids) > maxSupplementalGroups {
		return nil, fmt.Errorf("supplementalGroups accepts at most %d entries", maxSupplementalGroups)
	}
	sorted := append([]int64(nil), gids...)
	slices.Sort(sorted)
	for i, gid := range sorted {
		if gid < 0 {
			return nil, fmt.Errorf("supplementalGroups entry %d must be a non-negative integer", gid)
		}
		if i > 0 && gid == sorted[i-1] {
			return nil, fmt.Errorf("supplementalGroups contains duplicate group ID %d", gid)
		}
	}
	return sorted, nil
}

func supplementalGID(value any) (int64, error) {
	switch n := value.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case int32:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		if n > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("supplementalGroups entry %d is out of range", n)
		}
		return int64(n), nil
	case float64:
		return gidFromFloat(n)
	case float32:
		return gidFromFloat(float64(n))
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, nil
		}
		f, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("supplementalGroups entry %q must be a numeric host group ID", n.String())
		}
		return gidFromFloat(f)
	case string:
		return 0, fmt.Errorf("supplementalGroups entry %q must be a numeric host group ID", n)
	default:
		return 0, fmt.Errorf("supplementalGroups entry %v must be a numeric host group ID", value)
	}
}

func gidFromFloat(n float64) (int64, error) {
	if math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
		return 0, fmt.Errorf("supplementalGroups entry %v must be a whole number", n)
	}
	if n < 0 {
		return 0, fmt.Errorf("supplementalGroups entry %v must be a non-negative integer", n)
	}
	// float64(MaxInt64) rounds to 2^63, which does not fit in int64.
	if n >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("supplementalGroups entry %v is out of range", n)
	}
	gid := int64(n)
	if float64(gid) != n {
		return 0, fmt.Errorf("supplementalGroups entry %v is out of range", n)
	}
	return gid, nil
}
