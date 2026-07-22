package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/akins/jarvis/internal/llm"
)

const (
	serperSearchURL = "https://google.serper.dev/search"
	serperNewsURL   = "https://google.serper.dev/news"
	serperScrapeURL = "https://scrape.serper.dev"
)

// webClient talks to Serper for search and page extraction.
type webClient struct {
	apiKey string
	http   *http.Client
}

// RegisterWeb adds search, news, page-fetch and deep-research skills.
// With no API key the skills still register but return a clear explanation,
// so the model is never left guessing why the world went quiet.
func RegisterWeb(r *Registry, serperKey string) {
	c := &webClient{
		apiKey: serperKey,
		http:   &http.Client{Timeout: 45 * time.Second},
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "web_search",
			Description: "Search the web for current information. Returns titles, links and snippets.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query": {Type: "string", Description: "The search query."},
				"num":   {Type: "number", Description: "Number of results, default 8, max 20."},
			}, "query"),
		},
		Handler: c.search,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "web_news",
			Description: "Search recent news articles on a topic.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query": {Type: "string", Description: "The news topic."},
				"num":   {Type: "number", Description: "Number of articles, default 6."},
			}, "query"),
		},
		Handler: c.news,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "web_fetch",
			Description: "Fetch a web page and return its readable text content.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"url":       {Type: "string", Description: "Full URL including scheme."},
				"max_chars": {Type: "number", Description: "Truncate to this many characters, default 12000."},
			}, "url"),
		},
		Handler: c.fetch,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "web_research",
			Description: "In-depth research: search a question, then fetch and read the top " +
				"results in parallel, returning their full text. Slower and more thorough " +
				"than web_search — use when snippets are not enough to answer properly.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query":  {Type: "string", Description: "The research question."},
				"depth":  {Type: "number", Description: "How many sources to read in full, default 3, max 6."},
				"budget": {Type: "number", Description: "Characters to keep per source, default 6000."},
			}, "query"),
		},
		Handler: c.research,
	})
}

// post sends a JSON body to a Serper endpoint and decodes the reply.
func (c *webClient) post(ctx context.Context, url string, body any, out any) error {
	if c.apiKey == "" {
		return fmt.Errorf("web search is unavailable: no SERPER_API_KEY configured")
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("serper http %d: %s", resp.StatusCode, clip(string(payload), 300))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

type serperSearchResult struct {
	AnswerBox *struct {
		Answer  string `json:"answer"`
		Snippet string `json:"snippet"`
		Title   string `json:"title"`
	} `json:"answerBox"`
	KnowledgeGraph *struct {
		Title       string `json:"title"`
		Type        string `json:"type"`
		Description string `json:"description"`
	} `json:"knowledgeGraph"`
	Organic []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
		Date    string `json:"date"`
	} `json:"organic"`
}

func (c *webClient) search(ctx context.Context, args map[string]any) (string, error) {
	query := argString(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	num := clamp(argInt(args, "num", 8), 1, 20)

	var res serperSearchResult
	if err := c.post(ctx, serperSearchURL, map[string]any{"q": query, "num": num}, &res); err != nil {
		return "", err
	}

	var sb strings.Builder
	if res.AnswerBox != nil {
		if a := firstNonBlank(res.AnswerBox.Answer, res.AnswerBox.Snippet); a != "" {
			sb.WriteString("Direct answer: " + a + "\n\n")
		}
	}
	if kg := res.KnowledgeGraph; kg != nil && kg.Description != "" {
		fmt.Fprintf(&sb, "%s (%s): %s\n\n", kg.Title, kg.Type, kg.Description)
	}
	if len(res.Organic) == 0 && sb.Len() == 0 {
		return "No results found.", nil
	}
	for i, o := range res.Organic {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, o.Title, o.Link)
		if o.Snippet != "" {
			fmt.Fprintf(&sb, "   %s", o.Snippet)
			if o.Date != "" {
				fmt.Fprintf(&sb, " (%s)", o.Date)
			}
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func (c *webClient) news(ctx context.Context, args map[string]any) (string, error) {
	query := argString(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	num := clamp(argInt(args, "num", 6), 1, 20)

	var res struct {
		News []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Date    string `json:"date"`
			Source  string `json:"source"`
			Snippet string `json:"snippet"`
		} `json:"news"`
	}
	if err := c.post(ctx, serperNewsURL, map[string]any{"q": query, "num": num}, &res); err != nil {
		return "", err
	}
	if len(res.News) == 0 {
		return "No news found.", nil
	}

	var sb strings.Builder
	for i, n := range res.News {
		fmt.Fprintf(&sb, "%d. %s — %s (%s)\n   %s\n", i+1, n.Title, n.Source, n.Date, n.Link)
		if n.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", n.Snippet)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func (c *webClient) fetch(ctx context.Context, args map[string]any) (string, error) {
	url := argString(args, "url")
	if url == "" {
		return "", fmt.Errorf("url is required")
	}
	maxChars := clamp(argInt(args, "max_chars", 12000), 500, 100000)

	text, err := c.scrape(ctx, url)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("no readable text extracted from %s", url)
	}
	return clip(text, maxChars), nil
}

func (c *webClient) scrape(ctx context.Context, url string) (string, error) {
	var res struct {
		Text string `json:"text"`
	}
	if err := c.post(ctx, serperScrapeURL, map[string]any{"url": url}, &res); err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Text), nil
}

// research searches, then reads the top results concurrently. Fetches run in
// parallel because they are network-bound and independent; a slow source
// should not serialise behind the others.
func (c *webClient) research(ctx context.Context, args map[string]any) (string, error) {
	query := argString(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	depth := clamp(argInt(args, "depth", 3), 1, 6)
	budget := clamp(argInt(args, "budget", 6000), 500, 40000)

	var res serperSearchResult
	if err := c.post(ctx, serperSearchURL, map[string]any{"q": query, "num": depth * 2}, &res); err != nil {
		return "", err
	}
	if len(res.Organic) == 0 {
		return "No sources found for that question.", nil
	}
	if len(res.Organic) > depth {
		res.Organic = res.Organic[:depth]
	}

	type source struct {
		title, link, text string
		err               error
	}
	sources := make([]source, len(res.Organic))

	var wg sync.WaitGroup
	for i, o := range res.Organic {
		wg.Add(1)
		go func(i int, title, link string) {
			defer wg.Done()
			sources[i] = source{title: title, link: link}
			text, err := c.scrape(ctx, link)
			sources[i].text, sources[i].err = text, err
		}(i, o.Title, o.Link)
	}
	wg.Wait()

	var sb strings.Builder
	fmt.Fprintf(&sb, "Research on %q — read %d sources.\n\n", query, len(sources))
	for i, s := range sources {
		fmt.Fprintf(&sb, "── Source %d: %s\n   %s\n", i+1, s.title, s.link)
		switch {
		case s.err != nil:
			// A dead source is information too; report it rather than hiding it.
			fmt.Fprintf(&sb, "   [could not read: %v]\n\n", s.err)
		case s.text == "":
			sb.WriteString("   [no readable text]\n\n")
		default:
			fmt.Fprintf(&sb, "%s\n\n", clip(s.text, budget))
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func firstNonBlank(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
