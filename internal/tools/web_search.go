package tools

import (
	"encoding/json"
	"strings"

	"github.com/brent/echo/internal/searxng"
)

const (
	defaultWebSearchResults = 5
	maxWebSearchResults     = 10
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "web_search",
			Description: "Search the web through the configured SearXNG endpoint for current public information.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query.",
					},
					"maxResults": map[string]any{
						"type":        "integer",
						"description": "Maximum search results to return. Defaults to 5 and is capped at 10.",
						"minimum":     1,
						"maximum":     maxWebSearchResults,
					},
				},
			},
		},
		Run: webSearch,
	})
}

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"maxResults"`
}

type webSearchOutput struct {
	Query       string            `json:"query"`
	ResultCount int               `json:"resultCount"`
	Results     []webSearchResult `json:"results"`
}

type webSearchResult struct {
	Title         string  `json:"title"`
	URL           string  `json:"url,omitempty"`
	Content       string  `json:"content,omitempty"`
	Engine        string  `json:"engine,omitempty"`
	PublishedDate string  `json:"publishedDate,omitempty"`
	Score         float64 `json:"score,omitempty"`
}

func webSearch(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args webSearchArgs
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "query is required"}
	}
	limit := args.MaxResults
	if limit <= 0 {
		limit = defaultWebSearchResults
	}
	if limit > maxWebSearchResults {
		limit = maxWebSearchResults
	}

	response, err := searxng.Search(ctx.context(), ctx.SearxngURL, args.Query)
	if err != nil {
		return nil, SafeError{Code: "search_failed", Message: err.Error()}
	}
	results := make([]webSearchResult, 0, min(limit, len(response.Results)))
	for _, result := range response.Results {
		if len(results) == limit {
			break
		}
		results = append(results, webSearchResult{
			Title:         result.Title,
			URL:           result.URL,
			Content:       result.Content,
			Engine:        result.Engine,
			PublishedDate: result.PublishedDate,
			Score:         result.Score,
		})
	}
	query := strings.TrimSpace(response.Query)
	if query == "" {
		query = args.Query
	}
	return webSearchOutput{
		Query:       query,
		ResultCount: len(results),
		Results:     results,
	}, nil
}
