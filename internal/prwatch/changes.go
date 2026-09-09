package prwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

func CloneSnapshot(snapshot Snapshot) (Snapshot, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	var clone Snapshot
	if err := decodeJSON(raw, &clone); err != nil {
		return nil, err
	}
	return clone, nil
}

func ChangesBetween(previous, current Snapshot) (Changes, error) {
	currentCopy, err := CloneSnapshot(current)
	if err != nil {
		return nil, fmt.Errorf("current snapshot: %w", err)
	}
	sortChecks(currentCopy["checks"].([]any))
	var previousCopy Snapshot
	if previous != nil {
		previousCopy, err = CloneSnapshot(previous)
		if err != nil {
			return nil, fmt.Errorf("previous snapshot: %w", err)
		}
		sortChecks(previousCopy["checks"].([]any))
	}

	changes := Changes{}
	for _, key := range []string{"head_sha", "state", "draft", "mergeable", "merge_state", "checks"} {
		if previousCopy == nil || !reflect.DeepEqual(previousCopy[key], currentCopy[key]) {
			var before any
			if previousCopy != nil {
				before = previousCopy[key]
			}
			changes[key] = map[string]any{"before": before, "after": currentCopy[key]}
		}
	}

	removedEvidence := map[string]any{"head_sha": nil}
	if previousCopy != nil {
		removedEvidence["head_sha"] = previousCopy["head_sha"]
	}
	for _, key := range []string{"comments", "reviews"} {
		oldItems := map[string]any{}
		if previousCopy != nil {
			oldItems = previousCopy[key].(map[string]any)
		}
		currentItems := currentCopy[key].(map[string]any)
		changed := changedItems(oldItems, currentItems)
		if previousCopy == nil && key == "comments" {
			changed = map[string]any{}
		}
		if previousCopy == nil && key == "reviews" {
			changed = initialRequestedReviews(currentItems)
		}
		if len(changed) != 0 {
			changes[key] = changed
		}
		removed := removedIDs(oldItems, currentItems)
		if len(removed) != 0 {
			changes[key+"_removed"] = removed
			evidence := map[string]any{}
			for _, id := range removed {
				evidence[id] = oldItems[id]
			}
			removedEvidence[key] = evidence
		}
	}

	oldThreads := map[string]any{}
	if previousCopy != nil {
		oldThreads = previousCopy["threads"].(map[string]any)
	}
	currentThreads := currentCopy["threads"].(map[string]any)
	threadChanges, removedReplies, err := changedThreads(oldThreads, currentThreads)
	if err != nil {
		return nil, err
	}
	if len(threadChanges) != 0 {
		changes["threads"] = threadChanges
	}
	removedThreads := removedIDs(oldThreads, currentThreads)
	if len(removedThreads) != 0 {
		changes["threads_removed"] = removedThreads
	}
	if len(removedReplies) != 0 {
		removedEvidence["thread_comments"] = removedReplies
	}
	if len(removedEvidence) > 1 {
		changes["removed_evidence"] = removedEvidence
	}
	return changes, nil
}

func changedItems(oldItems, currentItems map[string]any) map[string]any {
	changed := map[string]any{}
	for _, id := range sortedKeys(currentItems) {
		if !reflect.DeepEqual(oldItems[id], currentItems[id]) {
			changed[id] = currentItems[id]
		}
	}
	return changed
}

func removedIDs(oldItems, currentItems map[string]any) []string {
	removed := []string{}
	for id := range oldItems {
		if _, exists := currentItems[id]; !exists {
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	return removed
}

func initialRequestedReviews(reviews map[string]any) map[string]any {
	ids := sortedKeys(reviews)
	sort.SliceStable(ids, func(i, j int) bool {
		left := reviews[ids[i]].(map[string]any)
		right := reviews[ids[j]].(map[string]any)
		leftTime, _ := left["submitted_at"].(string)
		rightTime, _ := right["submitted_at"].(string)
		if leftTime != rightTime {
			return leftTime < rightTime
		}
		return paddedID(ids[i]) < paddedID(ids[j])
	})
	latest := map[string]string{}
	for _, id := range ids {
		review := reviews[id].(map[string]any)
		state, _ := review["state"].(string)
		if state != "CHANGES_REQUESTED" && state != "APPROVED" && state != "DISMISSED" {
			continue
		}
		author, _ := review["author"].(string)
		latest[author] = id
	}
	changed := map[string]any{}
	selected := make([]string, 0, len(latest))
	for _, id := range latest {
		selected = append(selected, id)
	}
	sort.Strings(selected)
	for _, id := range selected {
		if reviews[id].(map[string]any)["state"] == "CHANGES_REQUESTED" {
			changed[id] = reviews[id]
		}
	}
	return changed
}

func paddedID(id string) string {
	if len(id) >= 30 {
		return id
	}
	return strings.Repeat("0", 30-len(id)) + id
}

func changedThreads(oldThreads, currentThreads map[string]any) (map[string]any, map[string]any, error) {
	changed := map[string]any{}
	removedEvidence := map[string]any{}
	for _, threadID := range sortedKeys(currentThreads) {
		current := currentThreads[threadID].(map[string]any)
		currentRows := current["comments"].([]any)
		oldRaw, existed := oldThreads[threadID]
		if !existed {
			changed[threadID] = current
			continue
		}
		old := oldRaw.(map[string]any)
		oldRows := old["comments"].([]any)
		oldByID, err := commentsByID(oldRows)
		if err != nil {
			return nil, nil, err
		}
		currentByID, err := commentsByID(currentRows)
		if err != nil {
			return nil, nil, err
		}
		newRows := []any{}
		for _, row := range currentRows {
			id, err := evidenceID(row)
			if err != nil {
				return nil, nil, err
			}
			if !reflect.DeepEqual(oldByID[id], row) {
				newRows = append(newRows, row)
			}
		}
		outdatedChanged := !reflect.DeepEqual(old["outdated"], current["outdated"])
		if len(newRows) != 0 || outdatedChanged {
			thread := map[string]any{"outdated": current["outdated"], "comments": newRows}
			changed[threadID] = thread
		}
		removed := []string{}
		for id := range oldByID {
			if _, exists := currentByID[id]; !exists {
				removed = append(removed, id)
			}
		}
		sort.Strings(removed)
		if len(removed) != 0 {
			rows := make([]any, 0, len(removed))
			for _, id := range removed {
				rows = append(rows, oldByID[id])
			}
			removedEvidence[threadID] = rows
		}
	}
	return changed, removedEvidence, nil
}

func commentsByID(rows []any) (map[string]any, error) {
	comments := map[string]any{}
	for _, row := range rows {
		id, err := evidenceID(row)
		if err != nil {
			return nil, err
		}
		comments[id] = row
	}
	return comments, nil
}

func evidenceID(raw any) (string, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return "", errors.New("thread comment must be an object")
	}
	switch id := object["id"].(type) {
	case string:
		return id, nil
	case json.Number:
		return id.String(), nil
	default:
		return "", errors.New("thread comment id must be a string or number")
	}
}

func sortedKeys(items map[string]any) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
