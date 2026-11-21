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
	"github.com/nep76/mcpgo/terminal"
)

const selfName = "nep_mcpgo"
const selfVersion = "0.0.3"

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

type NoArgs struct{}

type ContainerSession struct {
	SessionId string `json:"session_id" jsonschema:"セッション識別子"`
}

type ContainerSessionStartJob struct {
	SessionId string `json:"session_id" jsonschema:"セッション識別子"`
	Command   string `json:"command" jsonschema:"Bash コマンドライン"`
}

type ContainerSessionJob struct {
	SessionId string `json:"session_id" jsonschema:"セッション識別子"`
	JobId     string `json:"job_id" jsonschema:"ジョブ識別子"`
}

type ContainerSessionJobStatus struct {
	Status string `json:"status" jsonschema:"ジョブステータス"`
}

type ContainerSessionJobResult struct {
	Status string `json:"status" jsonschema:"ジョブステータス"`
	Output string `json:"output" jsonschema:"コマンドの出力"`
}

func container_session_create(
	ctx context.Context,
	req *mcp.CallToolRequest,
	_ NoArgs,
) (*mcp.CallToolResult, ContainerSession, error) {
	session, err := terminal.CreateSession("qwen-test")
	if err != nil {
		return nil, ContainerSession{}, fmt.Errorf("Failed to create session %s", err)
	}
	return nil, ContainerSession{
		SessionId: session,
	}, nil
}

func container_session_start_job(
	ctx context.Context,
	req *mcp.CallToolRequest,
	job ContainerSessionStartJob,
) (*mcp.CallToolResult, ContainerSessionJob, error) {
	t := terminal.GetSession(job.SessionId)
	if t == nil {
		return nil, ContainerSessionJob{}, fmt.Errorf("Failed to get session")
	}

	j, err := t.Run(job.SessionId, job.Command)
	if err != nil {
		return nil, ContainerSessionJob{}, err
	}

	return nil, ContainerSessionJob{
		SessionId: job.SessionId,
		JobId:     j,
	}, nil
}

func container_session_query_job(
	ctx context.Context,
	req *mcp.CallToolRequest,
	job ContainerSessionJob,
) (*mcp.CallToolResult, ContainerSessionJobStatus, error) {
	t := terminal.GetSession(job.SessionId)
	if t == nil {
		return nil, ContainerSessionJobStatus{}, fmt.Errorf("Failed to get session")
	}
	r, err := t.QueryStatus(job.JobId)
	if err != nil {
		return nil, ContainerSessionJobStatus{}, err
	}

	return nil, ContainerSessionJobStatus{
		Status: r.Status,
	}, nil
}

func container_session_result_job(
	ctx context.Context,
	req *mcp.CallToolRequest,
	job ContainerSessionJob,
) (*mcp.CallToolResult, ContainerSessionJobResult, error) {
	t := terminal.GetSession(job.SessionId)
	if t == nil {
		return nil, ContainerSessionJobResult{}, fmt.Errorf("Failed to get session")
	}
	r, err := t.PopResult(job.JobId)
	if err != nil {
		return nil, ContainerSessionJobResult{}, fmt.Errorf("Failed to get job status: %s", err)
	}

	return nil, ContainerSessionJobResult{
		Status: r.Status,
		Output: *r.Output,
	}, nil
}

func container_session_close(
	ctx context.Context,
	req *mcp.CallToolRequest,
	session ContainerSession,
) (*mcp.CallToolResult, ContainerSession, error) {
	t := terminal.GetSession(session.SessionId)
	if t == nil {
		return nil, ContainerSession{}, fmt.Errorf("Invalid session id")
	}

	err := t.CloseSession()
	if err != nil {
		return nil, ContainerSession{}, nil
	}

	return nil, ContainerSession{
		SessionId: session.SessionId,
	}, err
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

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	http_req, err := http.NewRequestWithContext(ctx, http.MethodGet, req_url, nil)
	if err != nil {
		return nil, SearchOutput{}, err
	}

	resp, err := http.DefaultClient.Do(http_req)
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

	if len(data.Results) > 0 {
		log.Println(data.Results[0].Title)
	}
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

	mcp.AddTool(mcp_server, &mcp.Tool{
		Name: "shell_create_session",
		Description: "開発用のDebian Linux環境を操作するためのシェルセッションを起動し、セッション識別子を返します。" +
			"開発を指示された場合は、まずこのツールでセッションを開いてください。" +
			"セッションは最大10まで開くことができます。" +
			"ユーザーからの指示に対する作業が完了したと判断した時点で、必ずshell_close_sessionを呼び出してセッションを閉じてください。" +
			"ユーザーから追加の指示がある可能性は考慮せず、セッションを維持しないでください。",
	}, container_session_create)

	mcp.AddTool(mcp_server, &mcp.Tool{
		Name:        "shell_start_job",
		Description: "セッション識別子を指定してセッションでシェルコマンドを実行します。コマンドの実行ステータスと実行結果を問い合わせるためのジョブ識別子を返します。",
	}, container_session_start_job)

	mcp.AddTool(mcp_server, &mcp.Tool{
		Name: "shell_query_job",
		Description: "セッション識別子とジョブ識別子を指定して、過去に実行したコマンドの実行状態を問い合わせます。" +
			"pending = まだ実行が開始されていません、running = 実行中です、 done = 実行が完了しています。" +
			"done はプロセスの終了を示すものであり、コマンドの成功を意味するものではありません。終了コードや標準出力・標準エラーを確認するには shell_result_job を呼び出してください。",
	}, container_session_query_job)

	mcp.AddTool(mcp_server, &mcp.Tool{
		Name: "shell_result_job",
		Description: "セッション識別子とジョブ識別子を指定して、過去に実行したコマンドの実行結果を受け取ります。" +
			"最後の実行ステータス、終了コード、およびコマンドの出力を返します。一度結果を受け取ると当該ジョブ識別子は無効になります。" +
			"重要：ジョブの結果は必ず回収してください。shell_start_job でジョブを開始したら、実行結果が不要な場合でも、必ず shell_result_job を呼び出して結果を回収してください。" +
			"結果を回収しない場合、ジョブのデータが破棄されません。" +
			"shell_query_job で done を確認した後に shell_result_job を呼び出してください。" +
			"結果がまだ取得可能な状態でない場合はエラーが返ることがあります。その場合は、時間をおいて再度 shell_result_job を呼び出し、結果を正常に受け取れるまで回収を続けてください。" +
			"エラーが返っただけでジョブの回収を中止しないでください。",
	}, container_session_result_job)

	mcp.AddTool(mcp_server, &mcp.Tool{
		Name: "shell_close_session",
		Description: "指定されたセッション識別子を閉じてリソースを開放します。shell_create_sessionで開いたセッションは終了までに必ず閉じてください。" +
			"閉じることに成功すると閉じたセッション識別子をそのまま返します。成否に関わらず、当該セッション識別子は無効になります。",
	}, container_session_close)

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
