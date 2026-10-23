// Package decision provides a small, isolated client for a "Decision Model":
// a fast AI endpoint that answers a batch of typed questions about a piece of
// state and returns a probability distribution for each possible outcome.
//
// The package is intentionally self-contained: it depends only on the Go
// standard library so any Echo subsystem (chat, tools, notifications)
// can call it without creating import cycles or scattering business logic.
// Consumers use the fluent builder (Client.Ask) and the typed accessors on
// Response/Answer instead of hand-rolling request JSON.
//
// Wire contract (POST <base>/systemone):
//
//	{"model":"clef-flash","state":"...","questions":{"team":{"type":"choice",...}}}
//
// The response carries per-question answers with probabilities, confidence,
// and token usage. See the sample fixtures in client_test.go.
package decision

import "encoding/json"

// QuestionKind enumerates the supported question types.
type QuestionKind string

const (
	// KindChoice picks exactly one option from a labeled criteria map.
	KindChoice QuestionKind = "choice"
	// KindNoul answers a yes/no ("no or yes?") question with a single
	// probability in [0,1].
	KindNoul QuestionKind = "noul"
	// KindScore produces a weighted score over an ordered legend plus the
	// full probability distribution across legend indices.
	KindScore QuestionKind = "score"
)

// Criteria carries the payload for a question's "criteria" field. The wire
// format reuses the same JSON key for two shapes: choice questions take an
// object mapping option label to description, score questions take an array
// of ordered legend entries. Criteria marshals and unmarshals both.
type Criteria struct {
	// Options holds ordered legend entries for score questions.
	Options []string
	// Descriptions maps option label to description for choice questions.
	Descriptions map[string]string
}

func (c Criteria) MarshalJSON() ([]byte, error) {
	if len(c.Descriptions) > 0 {
		return json.Marshal(c.Descriptions)
	}
	if len(c.Options) > 0 {
		return json.Marshal(c.Options)
	}
	return []byte("null"), nil
}

func (c *Criteria) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*c = Criteria{}
		return nil
	}
	var descriptions map[string]string
	if err := json.Unmarshal(data, &descriptions); err == nil && descriptions != nil {
		*c = Criteria{Descriptions: descriptions}
		return nil
	}
	var options []string
	if err := json.Unmarshal(data, &options); err != nil {
		return err
	}
	*c = Criteria{Options: options}
	return nil
}

// Question is one typed question sent to the decision model.
type Question struct {
	Type         QuestionKind `json:"type"`
	Instructions string       `json:"instructions,omitempty"`
	Criteria     *Criteria    `json:"criteria,omitempty"`
}

// Request is the full decision-model request body.
type Request struct {
	Model     string              `json:"model"`
	State     string              `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Usage reports token accounting for the call. Decision models typically
// report zero output tokens because the reply is computed, not generated.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Answer is the decision-model reply for a single question. Which fields are
// populated depends on the question kind: choice fills Choice/Probabilities/
// Confidence, noul fills Noul, score fills Score/Legend/Probabilities/
// Confidence.
type Answer struct {
	Type          QuestionKind       `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
}

// Best returns the highest-probability option for a choice or score answer.
// For score answers the legend index is returned as a string ("0", "1", ...).
// The second return is false when the answer carries no probabilities.
func (a Answer) Best() (string, float64, bool) {
	bestKey := ""
	bestProb := 0.0
	found := false
	for key, prob := range a.Probabilities {
		if !found || prob > bestProb {
			bestKey, bestProb, found = key, prob, true
		}
	}
	return bestKey, bestProb, found
}

// Probability returns the probability assigned to a specific option, or 0
// when the option is absent from the distribution.
func (a Answer) Probability(key string) float64 {
	return a.Probabilities[key]
}

// Response is the full decision-model reply.
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Answer returns the answer for a named question and whether it exists.
func (r Response) Answer(name string) (Answer, bool) {
	answer, ok := r.Answers[name]
	return answer, ok
}
