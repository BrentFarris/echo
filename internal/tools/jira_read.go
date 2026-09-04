package tools

import (
	"encoding/json"
	"strings"

	"github.com/brent/echo/internal/jira"
)

const (
	defaultJiraResults = 10
	maxJiraResults     = 50
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "jira_read",
			Description: "Read JIRA issues using a JQL query. Use key = PROJ-123 to get a single issue, assignee = currentUser() for your active tasks, or any valid JQL expression.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "JQL query (e.g. key = PROJ-123, assignee = currentUser()).",
					},
					"fields": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Field names to include. Defaults to key,summary,description,status,assignee,priority,comment.",
					},
					"maxResults": map[string]any{
						"type":        "integer",
						"description": "Maximum results to return. Defaults to 10, max 50.",
						"minimum":     1,
						"maximum":     maxJiraResults,
					},
				},
			},
		},
		Run: jiraRead,
	})
}

type jiraReadArgs struct {
	Query      string   `json:"query"`
	Fields     []string `json:"fields"`
	MaxResults int      `json:"maxResults"`
}

type jiraReadOutput struct {
	ResultCount int    `json:"resultCount"`
	Total       int    `json:"total"`
	Query       string `json:"query"`
	IssuesJSON  string `json:"issuesJson"`
}

func jiraRead(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args jiraReadArgs
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "query is required"}
	}
	if ctx.JiraHost == "" {
		return nil, SafeError{Code: "not_configured", Message: "Jira is not configured in settings"}
	}

	client, err := jira.New(ctx.JiraHost, ctx.JiraUsername, ctx.JiraAPIToken)
	if err != nil {
		return nil, SafeError{Code: "client_error", Message: err.Error()}
	}

	limit := args.MaxResults
	if limit <= 0 {
		limit = defaultJiraResults
	}
	if limit > maxJiraResults {
		limit = maxJiraResults
	}

	result, err := client.Search(ctx.context(), args.Query, args.Fields, limit)
	if err != nil {
		return nil, SafeError{Code: "search_failed", Message: err.Error()}
	}

	actualCount := len(result.Issues)
	if actualCount > limit {
		actualCount = limit
	}

	return jiraReadOutput{
		ResultCount: actualCount,
		Total:       result.Total,
		Query:       args.Query,
		IssuesJSON:  result.MinimalJSON(),
	}, nil
}
