package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const selfName = "nep_mcpgo"
const selfVersion = "0.0.1"

type SearchInput struct {
	Query string `json:"query" jsonschema:"検索キーワード"`
}

type SearchOutput struct {
	Results string `json:"results" jsonschema:"検索結果"`
}

type FetchURL struct {
	URL string `json:"url" jsonschema:"取得したいURL"`
}

type FetchDocument struct {
	Results string `json:"results" jsonschema:"文書内容"`
}

type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type SearXNGResponse struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
}

func web_fetch(
	ctx context.Context,
	req *mcp.CallToolRequest,
	fetch FetchURL,
) (*mcp.CallToolResult, FetchDocument, error) {
	const maxContentLength = 5 * 1024 * 1024

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	http_req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetch.URL, nil)
	if err != nil {
		return nil, FetchDocument{}, err
	}

	resp, err := http.DefaultClient.Do(http_req)
	if err != nil {
		return nil, FetchDocument{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, FetchDocument{}, fmt.Errorf("URL unavailable: %s", resp.Status)
	}

	if resp.ContentLength > maxContentLength {
		return nil, FetchDocument{}, fmt.Errorf("document too long")
	}

	content_type := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(content_type, "text/") {
		return nil, FetchDocument{}, fmt.Errorf("unsupported content type: %s", content_type)
	}

	//base_url, err := url.Parse(fetch.URL)
	//if err != nil {
	//	return nil, FetchDocument{}, err
	//}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxContentLength+1))
	if err != nil {
		return nil, FetchDocument{}, err
	}
	if len(body) > maxContentLength {
		return nil, FetchDocument{}, fmt.Errorf("document too long")
	}

	rmtags := regexp.MustCompile(`(?is)<a\s+.*?href=["'](.*?)["']\s*>(.*?)</a\s*>`)
	doc := rmtags.ReplaceAllStringFunc(string(body), func(match string) string {
		matches := rmtags.FindStringSubmatch(match)

		/* 無限に再帰しかねないので一旦保留
		// hrefをかっこ書きで負荷
		href, err := url.Parse(matches[1])
		if err != nil {
			return matches[2]
		}

		fullpath := base_url.ResolveReference(href)
		return matches[2] + "(" + fullpath.String() + ")"
		*/
		return matches[2]
	})

	rmtags = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)\s*>`)
	doc = rmtags.ReplaceAllString(doc, " ")
	rmtags = regexp.MustCompile(`<.*?>`)
	doc = rmtags.ReplaceAllString(doc, " ")
	doc = html.UnescapeString(doc)
	doc = strings.ReplaceAll(doc, "\u00a0", " ")
	rmtags = regexp.MustCompile(`\s+`)
	doc = rmtags.ReplaceAllString(doc, " ")
	doc = strings.TrimSpace(doc)

	log.Printf("web_fetch: %s", fetch.URL)

	return nil, FetchDocument{
		Results: doc,
	}, nil
}

func web_search(
	ctx context.Context,
	req *mcp.CallToolRequest,
	input SearchInput,
) (*mcp.CallToolResult, SearchOutput, error) {
	query := url.Values{}
	query.Set("q", input.Query)
	query.Set("format", "json")
	req_url := "http://localhost:18080/search?" + query.Encode()

	resp, err := http.Get(req_url)
	if err != nil {
		return nil, SearchOutput{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, SearchOutput{}, fmt.Errorf("web_search temporary unavailable")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, SearchOutput{}, err
	}

	var data SearXNGResponse
	err = json.Unmarshal(body, &data)
	if err != nil {
		return nil, SearchOutput{}, err
	}

	log.Println(data.Results[0].Title)

	var results string
	for _, item := range data.Results {
		results += item.Title + "\n"
		results += item.URL + "\n"
		results += item.Content + "\n\n"
	}

	return nil, SearchOutput{
		Results: results,
	}, nil
}

func main() {
	mcp_server := mcp.NewServer(
		&mcp.Implementation{
			Name:    selfName,
			Version: selfVersion,
		},
		&mcp.ServerOptions{},
	)

	mcp.AddTool(mcp_server, &mcp.Tool{
		Name: "web_search",
		Description: "インターネット上の情報を検索します。" +
			"最新情報や、手元の知識だけでは正確に回答できない情報を調べる場合に使用してください。" +
			"ページの詳しい内容を確認する必要がある場合はweb_fetchを使ってページ本文を取得してください。" +
			"取得できなかった場合は、その理由を報告してください。",
	}, web_search)

	mcp.AddTool(mcp_server, &mcp.Tool{
		Name: "web_fetch",
		Description: "指定されたURLからファイルやWebページをHTTP GETでダウンロードし、その内容をテキストとして返します。" +
			"web_searchの検索結果に含まれているURLをダウンロードすることもできます。" +
			"ユーザーがURLを指定して、そのファイルやページの内容について質問した場合は、必ずこのツールを使用して実際の内容を取得してください。" +
			"URLやファイル名から内容を推測してはいけません。" +
			"取得できなかった場合は、その理由を報告してください。",
	}, web_fetch)

	http_server := mcp.NewStreamableHTTPHandler(
		func(req *http.Request) *mcp.Server {
			return mcp_server
		}, &mcp.StreamableHTTPOptions{Stateless: true},
	)

	server_mux := http.NewServeMux()
	server_mux.Handle("/mcp", http_server)

	server := &http.Server{
		Addr: ":28080",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version")
			w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			server_mux.ServeHTTP(w, r)
		}),
	}

	log.Printf("%s - %s\n", selfName, selfVersion)

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
