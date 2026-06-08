package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"todo-assistant/internal/config"
	"todo-assistant/internal/memo"
	"todo-assistant/internal/todo"

	trpcmemoryinmem "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
)

func TestOpenAIClientWithMockServer(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Fatalf("bad auth %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		requests++
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Fatalf("request did not enable streaming: %s", string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			if !strings.Contains(string(body), `"tools"`) || !strings.Contains(string(body), `"todo_create"`) {
				t.Fatalf("request did not include OpenAI tools: %s", string(body))
			}
			writeSSE(w, `{"id":"cmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"todo_create","arguments":"{\"title\":\"交周报\",\"priority\":\"high\"}"}}]},"finish_reason":null}]}`)
			writeSSE(w, `{"id":"cmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			writeSSE(w, `[DONE]`)
			return
		}
		if !strings.Contains(string(body), `"role":"tool"`) || !strings.Contains(string(body), `"call_1"`) {
			t.Fatalf("request did not include tool result: %s", string(body))
		}
		writeSSE(w, `{"id":"cmpl_2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"已帮你"},"finish_reason":null}]}`)
		writeSSE(w, `{"id":"cmpl_2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"记下：交周报。"},"finish_reason":null}]}`)
		writeSSE(w, `{"id":"cmpl_2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		writeSSE(w, `[DONE]`)
	}))
	defer srv.Close()
	client := NewOpenAIClient(config.ModelConfig{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "test"})
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	got, err := client.ChatWithTools(context.Background(), "你是待办助手", "帮我记一下交周报", todo.NewToolExecutor(svc, loc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "交周报") {
		t.Fatalf("unexpected completion %q", got)
	}
	if requests != 2 {
		t.Fatalf("expected 2 SDK requests, got %d", requests)
	}
	items, err := svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "交周报" || items[0].Priority != todo.PriorityHigh {
		t.Fatalf("tool call did not create todo: %#v", items)
	}
}

func writeSSE(w http.ResponseWriter, data string) {
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func TestAgentRuleBasedCreate(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	a := New(nil, svc, loc)
	reply, err := a.HandleText(context.Background(), "提醒我明天下午三点交周报 优先级高")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已创建") {
		t.Fatalf("unexpected reply %q", reply)
	}
	items, err := svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DueAt == nil {
		t.Fatalf("unexpected items %#v", items)
	}
}

func TestAgentDeleteReminderDoesNotCreateTodo(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	a := New(nil, svc, loc)

	reply, err := a.HandleText(context.Background(), "删除提醒")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, "已创建") {
		t.Fatalf("delete intent created a todo: %q", reply)
	}
	items, err := svc.List(todo.Filter{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("delete intent should not create todos: %#v", items)
	}
}

func TestAgentDeleteReminderByTitleKeyword(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	if _, err := svc.Create(todo.CreateInput{Title: "完成 Phison 的 DLL 集成版本测试并反馈测试结果"}); err != nil {
		t.Fatal(err)
	}
	a := New(nil, svc, loc)

	reply, err := a.HandleText(context.Background(), "删除 phison 的提醒")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已删除") {
		t.Fatalf("unexpected reply %q", reply)
	}
	items, err := svc.List(todo.Filter{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != todo.StatusDeleted {
		t.Fatalf("todo was not deleted: %#v", items)
	}
}

func TestAgentLLMIntentBulkDeleteByKeyword(t *testing.T) {
	var requests int
	var deleteIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if !strings.Contains(string(body), "意图分类器") {
				t.Fatalf("expected intent classifier request, got %s", string(body))
			}
			fmt.Fprint(w, `{"id":"intent_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"op\":\"delete\",\"todo_related\":true,\"confidence\":0.97}"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 2:
			if !strings.Contains(string(body), `"todo_list"`) || !strings.Contains(string(body), `"todo_delete"`) {
				t.Fatalf("expected delete runtime tools, got %s", string(body))
			}
			writeSSE(w, `{"id":"cmpl_list","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_list","type":"function","function":{"name":"todo_list","arguments":"{\"status\":\"open\"}"}}]},"finish_reason":null}]}`)
			writeSSE(w, `{"id":"cmpl_list","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			writeSSE(w, `[DONE]`)
		case 3:
			if !strings.Contains(string(body), `"role":"tool"`) || !strings.Contains(string(body), `"call_list"`) {
				t.Fatalf("expected list tool result, got %s", string(body))
			}
			fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"id":"cmpl_delete","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_del_1","type":"function","function":{"name":"todo_delete","arguments":"{\"id\":\"%s\"}"}},{"index":1,"id":"call_del_2","type":"function","function":{"name":"todo_delete","arguments":"{\"id\":\"%s\"}"}}]},"finish_reason":null}]}`, deleteIDs[0], deleteIDs[1]))
			writeSSE(w, `{"id":"cmpl_delete","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			writeSSE(w, `[DONE]`)
		case 4:
			if !strings.Contains(string(body), `"call_del_1"`) || !strings.Contains(string(body), `"call_del_2"`) {
				t.Fatalf("expected delete tool results, got %s", string(body))
			}
			writeSSE(w, `{"id":"cmpl_final","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"已删除 2 条喝水相关待办。"},"finish_reason":null}]}`)
			writeSSE(w, `{"id":"cmpl_final","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(w, `[DONE]`)
		default:
			t.Fatalf("unexpected request %d: %s", requests, string(body))
		}
	}))
	defer srv.Close()

	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	if _, err := svc.Create(todo.CreateInput{Title: "早上喝水"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(todo.CreateInput{Title: "下午喝水"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(todo.CreateInput{Title: "交周报"}); err != nil {
		t.Fatal(err)
	}
	openItems, err := svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range openItems {
		if strings.Contains(item.Title, "喝水") {
			deleteIDs = append(deleteIDs, item.ID)
		}
	}
	if len(deleteIDs) != 2 {
		t.Fatalf("expected two delete IDs, got %#v from %#v", deleteIDs, openItems)
	}
	model := NewOpenAIClient(config.ModelConfig{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "test"})
	a := New(model, svc, loc)

	reply, err := a.HandleText(context.Background(), "把喝水相关的待办都删掉")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已删除 2 条") {
		t.Fatalf("unexpected reply %q", reply)
	}
	if requests != 4 {
		t.Fatalf("expected intent classification plus LLM tool loop requests, got %d", requests)
	}
	items, err := svc.List(todo.Filter{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	var deleted, open int
	for _, item := range items {
		switch item.Status {
		case todo.StatusDeleted:
			deleted++
		case todo.StatusOpen:
			open++
			if item.Title != "交周报" {
				t.Fatalf("unexpected open item %#v", item)
			}
		}
	}
	if deleted != 2 || open != 1 {
		t.Fatalf("unexpected item statuses: deleted=%d open=%d items=%#v", deleted, open, items)
	}
}

func TestAgentIntentClassifierFallsBackToRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"intent_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"not-json"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	model := NewOpenAIClient(config.ModelConfig{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "test"})
	a := New(model, svc, loc)

	reply, err := a.HandleText(context.Background(), "提醒我明天下午三点交周报")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已创建") {
		t.Fatalf("fallback did not create todo: %q", reply)
	}
	items, err := svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "交周报" {
		t.Fatalf("unexpected fallback items %#v", items)
	}
}

func TestAgentTimeUpdateByTitleKeywordIsNotList(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	if _, err := svc.Create(todo.CreateInput{Title: "完成 Phison 的 DLL 集成版本测试并反馈测试结果"}); err != nil {
		t.Fatal(err)
	}
	a := New(nil, svc, loc)

	reply, err := a.HandleText(context.Background(), "帮我把 phison 的待办 截止时间改成下午五点")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已更新") {
		t.Fatalf("unexpected reply %q", reply)
	}
	items, err := svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DueAt == nil {
		t.Fatalf("todo due was not updated: %#v", items)
	}
	if got := items[0].DueAt.In(loc); got.Hour() != 17 || got.Minute() != 0 {
		t.Fatalf("want 17:00 due, got %s", got.Format("2006-01-02 15:04"))
	}
}

func TestAgentGuardBlocksCreateToolForDeleteIntent(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	a := New(nil, svc, loc)
	intent := analyzeIntent("删除提醒")

	_, err = a.executeTool(context.Background(), intent, todo.ToolCall{
		Name: "todo.create",
		Args: map[string]interface{}{"title": "删除提醒"},
	})
	if err == nil {
		t.Fatal("expected guard to block create tool for delete intent")
	}
	items, err := svc.List(todo.Filter{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("blocked tool should not create todos: %#v", items)
	}
}

func TestAgentGreetingIsConversational(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	a := New(nil, svc, loc)
	reply, err := a.HandleText(context.Background(), "你好")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "你好") {
		t.Fatalf("unexpected reply %q", reply)
	}
	items, err := svc.List(todo.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("greeting should not create todos: %#v", items)
	}
}

func TestAgentMemoryCommandsAddListDelete(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	memorySvc := trpcmemoryinmem.NewMemoryService()
	a := New(nil, svc, loc)
	a.runtime = &AgentRuntime{memorySvc: memorySvc}

	reply, err := a.HandleText(context.Background(), "/memory add prefers terse replies")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "prefers terse replies") {
		t.Fatalf("add reply did not include memory text: %q", reply)
	}

	reply, err = a.HandleText(context.Background(), "/memory list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "prefers terse replies") {
		t.Fatalf("list reply did not include memory text: %q", reply)
	}

	entries, err := a.runtime.ListMemories(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] == nil || entries[0].ID == "" {
		t.Fatalf("unexpected memory entries: %#v", entries)
	}

	reply, err = a.HandleText(context.Background(), "/memory delete "+shortMemoryID(entries[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "prefers terse replies") {
		t.Fatalf("delete reply did not include deleted memory text: %q", reply)
	}

	entries, err = a.runtime.ListMemories(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("memory was not deleted: %#v", entries)
	}
}

func TestAgentLLMMemoCreateDoesNotCreateTodo(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			fmt.Fprint(w, `{"id":"intent_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"op\":\"memo_create\",\"todo_related\":true,\"confidence\":0.98}"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 2:
			if !strings.Contains(string(body), `"memo_create"`) || strings.Contains(string(body), `"todo_create"`) {
				t.Fatalf("expected only memo create tool, got %s", string(body))
			}
			writeSSE(w, `{"id":"cmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_memo","type":"function","function":{"name":"memo_create","arguments":"{\"title\":\"Phison 项目地址\",\"content\":\"项目地址是 https://example.com/phison\",\"tags\":[\"phison\"]}"}}]},"finish_reason":null}]}`)
			writeSSE(w, `{"id":"cmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			writeSSE(w, `[DONE]`)
		case 3:
			if !strings.Contains(string(body), `"role":"tool"`) || !strings.Contains(string(body), `"call_memo"`) {
				t.Fatalf("expected memo tool result, got %s", string(body))
			}
			writeSSE(w, `{"id":"cmpl_2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"已保存 Phison 项目地址。"},"finish_reason":null}]}`)
			writeSSE(w, `{"id":"cmpl_2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(w, `[DONE]`)
		default:
			t.Fatalf("unexpected request %d: %s", requests, string(body))
		}
	}))
	defer srv.Close()

	loc := time.FixedZone("CST", 8*3600)
	todoStore, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	memoStore, err := memo.NewStore(filepath.Join(t.TempDir(), "memos.json"))
	if err != nil {
		t.Fatal(err)
	}
	todoSvc := todo.NewService(todoStore, loc)
	memoSvc := memo.NewService(memoStore, loc)
	model := NewOpenAIClient(config.ModelConfig{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "test"})
	a := NewWithMemo(model, todoSvc, memoSvc, loc)

	reply, err := a.HandleText(context.Background(), "记一下 phison 项目地址是 https://example.com/phison")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "Phison") {
		t.Fatalf("unexpected reply %q", reply)
	}
	todos, err := todoSvc.List(todo.Filter{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 0 {
		t.Fatalf("memo create should not create todo: %#v", todos)
	}
	memos, err := memoSvc.List(memo.Filter{Query: "phison"})
	if err != nil {
		t.Fatal(err)
	}
	if len(memos) != 1 || len(memos[0].Links) != 1 {
		t.Fatalf("memo was not saved with link: %#v", memos)
	}
}

func TestAgentLLMMemoDeleteSearchesBeforeDelete(t *testing.T) {
	var requests int
	var memoID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			fmt.Fprint(w, `{"id":"intent_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"op\":\"memo_delete\",\"todo_related\":true,\"confidence\":0.97}"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch requests {
		case 2:
			if !strings.Contains(string(body), `"memo_search"`) || !strings.Contains(string(body), `"memo_delete"`) || strings.Contains(string(body), `"todo_delete"`) {
				t.Fatalf("expected memo search/delete tools, got %s", string(body))
			}
			writeSSE(w, `{"id":"cmpl_search","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_search","type":"function","function":{"name":"memo_search","arguments":"{\"query\":\"phison 链接\"}"}}]},"finish_reason":null}]}`)
			writeSSE(w, `{"id":"cmpl_search","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			writeSSE(w, `[DONE]`)
		case 3:
			if !strings.Contains(string(body), `"call_search"`) {
				t.Fatalf("expected search result, got %s", string(body))
			}
			fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"id":"cmpl_delete","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_delete","type":"function","function":{"name":"memo_delete","arguments":"{\"id\":\"%s\"}"}}]},"finish_reason":null}]}`, memoID))
			writeSSE(w, `{"id":"cmpl_delete","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			writeSSE(w, `[DONE]`)
		case 4:
			if !strings.Contains(string(body), `"call_delete"`) {
				t.Fatalf("expected delete result, got %s", string(body))
			}
			writeSSE(w, `{"id":"cmpl_final","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"已删除 Phison 链接备忘录。"},"finish_reason":null}]}`)
			writeSSE(w, `{"id":"cmpl_final","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(w, `[DONE]`)
		default:
			t.Fatalf("unexpected request %d: %s", requests, string(body))
		}
	}))
	defer srv.Close()

	loc := time.FixedZone("CST", 8*3600)
	todoStore, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	memoStore, err := memo.NewStore(filepath.Join(t.TempDir(), "memos.json"))
	if err != nil {
		t.Fatal(err)
	}
	todoSvc := todo.NewService(todoStore, loc)
	memoSvc := memo.NewService(memoStore, loc)
	existing, err := memoSvc.Create(memo.CreateInput{Title: "Phison 项目链接", Content: "https://example.com/phison", Tags: []string{"phison"}})
	if err != nil {
		t.Fatal(err)
	}
	memoID = existing.ID
	model := NewOpenAIClient(config.ModelConfig{BaseURL: srv.URL + "/v1", APIKey: "key", Model: "test"})
	a := NewWithMemo(model, todoSvc, memoSvc, loc)

	reply, err := a.HandleText(context.Background(), "把 phison 那条链接删掉")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已删除") {
		t.Fatalf("unexpected reply %q", reply)
	}
	items, err := memoSvc.List(memo.Filter{Query: "phison"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("deleted memo should be hidden: %#v", items)
	}
}

func TestAgentReminderAboutLinkStillCreatesTodoWithoutModel(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	store, err := todo.NewStore(filepath.Join(t.TempDir(), "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc := todo.NewService(store, loc)
	a := New(nil, svc, loc)

	reply, err := a.HandleText(context.Background(), "提醒我明天看 https://example.com/phison")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply, "已创建") {
		t.Fatalf("unexpected reply %q", reply)
	}
	items, err := svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected todo reminder, got %#v", items)
	}
}
