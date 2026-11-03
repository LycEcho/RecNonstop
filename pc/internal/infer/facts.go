package infer

import (
	"encoding/json"
	"strings"
)

// A model may omit an action or risk while condensing many chunks. Preserve
// every explicitly extracted leaf fact outside that lossy reduction. Final
// model facts are not added: only leaves have directly analyzed the transcript.
// Only whitespace is normalized for exact deduplication;
// different speakers, deadlines or wording remain distinct.
func mergeAnalysisFacts(final *Analysis, leaves []*Analysis) error {
	actions := make([]Action, 0)
	risks := make([]string, 0)
	constraints := make([]string, 0)
	seenActions := make(map[Action]bool)
	seenRisks := make(map[string]bool)
	seenConstraints := make(map[string]bool)
	normalize := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	add := func(a *Analysis) {
		for _, action := range a.Actions {
			key := Action{Speaker: normalize(action.Speaker), Owner: normalize(action.Owner), Action: normalize(action.Action), Details: normalize(action.Details)}
			if key.Action == "" && key.Details == "" || seenActions[key] {
				continue
			}
			seenActions[key] = true
			actions = append(actions, Action{
				Speaker: strings.TrimSpace(action.Speaker),
				Owner:   strings.TrimSpace(action.Owner),
				Action:  strings.TrimSpace(action.Action),
				Details: strings.TrimSpace(action.Details),
			})
		}
		for _, constraint := range a.Constraints {
			key := normalize(constraint)
			if key == "" || seenConstraints[key] {
				continue
			}
			seenConstraints[key] = true
			constraints = append(constraints, strings.TrimSpace(constraint))
		}
		for _, risk := range a.Risks {
			key := normalize(risk)
			if key == "" || seenRisks[key] {
				continue
			}
			seenRisks[key] = true
			risks = append(risks, strings.TrimSpace(risk))
		}
	}
	for _, leaf := range leaves {
		add(leaf)
	}
	// The pipeline persists Raw and daily reports read it, so updating only the
	// typed fields would leave reports with the model's incomplete fact set.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(final.Raw, &fields)
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	fields["summary"], _ = json.Marshal(final.Summary)
	fields["speakers"], _ = json.Marshal(final.Speakers)
	fields["actions"], _ = json.Marshal(actions)
	fields["risks"], _ = json.Marshal(risks)
	fields["constraints"], _ = json.Marshal(constraints)
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	final.Actions, final.Risks, final.Raw = actions, risks, raw
	final.Constraints = constraints
	return nil
}
