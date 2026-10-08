// Package fileclone shares identical file data without sharing writable inodes.
// The caller must exclusively own the destination until Share returns. Neither
// identity checks nor atomic rename can protect against concurrent writers.
package fileclone

import (
	"fmt"
	"sort"
	"strings"
)

const MinimumSize = 64 * 1024

// Report keeps logical payload separate from measured APFS private data. Private
// data is not volume free space: snapshots and delayed reclamation still apply.
type Report struct {
	Cloned              int
	LogicalBytes        uint64
	PrivateBytesReduced uint64
	Skipped             map[string]int
	Reason              string
	Errors              []string
}

func (r *Report) skip(reason string) {
	if r.Skipped == nil {
		r.Skipped = make(map[string]int)
	}
	r.Skipped[reason]++
}

func (r Report) String() string {
	if r.Reason != "" {
		return "APFS sharing skipped: " + r.Reason
	}
	parts := []string{fmt.Sprintf("APFS sharing: cloned=%d logical_bytes=%d private_data_reduced_bytes=%d", r.Cloned, r.LogicalBytes, r.PrivateBytesReduced)}
	keys := make([]string, 0, len(r.Skipped))
	for reason := range r.Skipped {
		keys = append(keys, reason)
	}
	sort.Strings(keys)
	for _, reason := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", reason, r.Skipped[reason]))
	}
	for _, err := range r.Errors {
		parts = append(parts, fmt.Sprintf("error=%q", err))
	}
	return strings.Join(parts, " ")
}
