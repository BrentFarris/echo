package decision

import (
	"context"
	"fmt"
)

// QueryBuilder assembles a decision-model request fluently so consumers never
// hand-roll question JSON. Obtain one with Client.Ask and finish with Do:
//
//	resp, err := client.Ask(ctx, state).
//	    Choice("team", "Which team?", map[string]string{"billing": "Payments"}).
//	    Noul("refund", "Does the customer request a refund?").
//	    Score("urgency", "How urgent?", []string{"Can wait", "Today"}).
//	    Do()
type QueryBuilder struct {
	client   *Client
	ctx      context.Context
	request  *Request
	firstErr error
}

// Ask starts a decision-model query about the given state. The returned
// builder records the first configuration error and surfaces it from Do, so
// chained calls never need per-step error checks.
func (c *Client) Ask(ctx context.Context, state string) *QueryBuilder {
	return &QueryBuilder{
		client:  c,
		ctx:     ctx,
		request: &Request{State: state, Questions: map[string]Question{}},
	}
}

// Choice adds a single-select question over labeled options. The criteria map
// pairs each option label with a short description the model uses to decide.
func (b *QueryBuilder) Choice(name, instructions string, criteria map[string]string) *QueryBuilder {
	return b.add(name, Question{
		Type:         KindChoice,
		Instructions: instructions,
		Criteria:     &Criteria{Descriptions: criteria},
	})
}

// Noul adds a yes/no question. The answer's Noul field carries the
// probability in [0,1].
func (b *QueryBuilder) Noul(name, instructions string) *QueryBuilder {
	return b.add(name, Question{Type: KindNoul, Instructions: instructions})
}

// Score adds a weighted-score question over an ordered legend. The answer's
// Score field carries the expected legend index and Probabilities carries the
// distribution across the stringified indices.
func (b *QueryBuilder) Score(name, instructions string, legend []string) *QueryBuilder {
	return b.add(name, Question{
		Type:         KindScore,
		Instructions: instructions,
		Criteria:     &Criteria{Options: legend},
	})
}

func (b *QueryBuilder) add(name string, question Question) *QueryBuilder {
	if b.request.Questions == nil {
		b.request.Questions = map[string]Question{}
	}
	if _, exists := b.request.Questions[name]; exists {
		b.failf("duplicate question %q", name)
		return b
	}
	b.request.Questions[name] = question
	return b
}

// Do sends the assembled request. It returns ErrDisabled when the client has
// no configured endpoint, and the first recorded configuration error when the
// builder was misused.
func (b *QueryBuilder) Do() (*Response, error) {
	if b.firstErr != nil {
		return nil, b.firstErr
	}
	if len(b.request.Questions) == 0 {
		return nil, ErrNoQuestions
	}
	return b.client.Classify(b.ctx, b.request)
}

func (b *QueryBuilder) failf(format string, args ...any) {
	if b.firstErr == nil {
		b.firstErr = fmt.Errorf("decision model: "+format, args...)
	}
}
