package searxng

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

const DefaultURL = "http://localhost:8080/"

type SearchResponse struct {
	Query               string     `json:"query"`
	NumberOfResults     int        `json:"number_of_results"`
	Results             []Result   `json:"results"`
	Answers             []any      `json:"answers"`
	Corrections         []any      `json:"corrections"`
	Infoboxes           []any      `json:"infoboxes"`
	Suggestions         []any      `json:"suggestions"`
	UnresponsiveEngines [][]string `json:"unresponsive_engines"`
}

type Result struct {
	URL           string   `json:"url,omitempty"`
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	PublishedDate string   `json:"publishedDate"`
	Engine        string   `json:"engine"`
	Template      string   `json:"template"`
	ParsedURL     []string `json:"parsed_url"`
	ImgSrc        string   `json:"img_src"`
	Thumbnail     string   `json:"thumbnail"`
	Priority      string   `json:"priority"`
	Engines       []string `json:"engines"`
	Positions     []int    `json:"positions"`
	Score         float64  `json:"score"`
	Category      string   `json:"category"`
}

func Search(ctx context.Context, baseURL string, q string) (SearchResponse, error) {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	u, err := url.ParseRequestURI(baseURL)
	if err != nil || u.Host == "" {
		return SearchResponse{}, errors.New("searxng url must be a valid HTTP or HTTPS URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return SearchResponse{}, errors.New("searxng url must use http or https")
	}

	query := u.Query()
	query.Set("q", q)
	query.Set("format", "json")
	u.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SearchResponse{}, errors.New("failed to create SearXNG request")
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return SearchResponse{}, errors.New("failed to get the response from SearXNG")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SearchResponse{}, fmt.Errorf("SearXNG returned HTTP %d", resp.StatusCode)
	}

	var res SearchResponse
	if err = json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return SearchResponse{}, errors.New("failed to decode response from SearXNG")
	}
	return res, nil
}
