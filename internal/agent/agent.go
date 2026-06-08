package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"todo-assistant/internal/config"
	"todo-assistant/internal/todo"

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	trpcmemory "trpc.group/trpc-go/trpc-agent-go/memory"
	trpcmemorydb "trpc.group/trpc-go/trpc-agent-go/memory/sqlite"
	trpcmodel "trpc.group/trpc-go/trpc-agent-go/model"
	trpcopenai "trpc.group/trpc-go/trpc-agent-go/model/openai"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	trpcsession "trpc.group/trpc-go/trpc-agent-go/session"
	trpcsessiondb "trpc.group/trpc-go/trpc-agent-go/session/sqlite"
	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"

	_ "modernc.org/sqlite"
)

const (
	agentAppName     = "todo-assistant"
	defaultUserID    = "todo-user"
	defaultSessionID = "todo-session"
)

type intentOp string

const (
	opChat     intentOp = "chat"
	opUnknown  intentOp = "unknown"
	opCreate   intentOp = "create"
	opList     intentOp = "list"
	opUpdate   intentOp = "update"
	opDelete   intentOp = "delete"
	opComplete intentOp = "complete"
	opReopen   intentOp = "reopen"
	opSnooze   intentOp = "snooze"
	opSummary  intentOp = "summary"
)

type inputIntent struct {
	Op          intentOp
	TodoRelated bool
	Original    string
}

type modelIntent struct {
	Op          string  `json:"op"`
	TodoRelated *bool   `json:"todo_related"`
	Confidence  float64 `json:"confidence"`
}

type intentContextKey struct{}

type Agent struct {
	model   *OpenAIClient
	runtime *AgentRuntime
	tools   *todo.ToolExecutor
	svc     *todo.Service
	loc     *time.Location
}

func New(model *OpenAIClient, svc *todo.Service, loc *time.Location) *Agent {
	if loc == nil {
		loc = time.Local
	}
	tools := todo.NewToolExecutor(svc, loc)
	var runtime *AgentRuntime
	if model != nil {
		runtime = NewAgentRuntime(model, tools, loc)
	}
	return &Agent{model: model, runtime: runtime, tools: tools, svc: svc, loc: loc}
}

func NewWithSQLite(model *OpenAIClient, svc *todo.Service, loc *time.Location, dataDir string) (*Agent, error) {
	if loc == nil {
		loc = time.Local
	}
	tools := todo.NewToolExecutor(svc, loc)
	runtime, err := NewSQLiteAgentRuntime(model, tools, loc, dataDir)
	if err != nil {
		return nil, err
	}
	return &Agent{model: model, runtime: runtime, tools: tools, svc: svc, loc: loc}, nil
}

func (a *Agent) Close() error {
	if a == nil || a.runtime == nil {
		return nil
	}
	return a.runtime.Close()
}

func (a *Agent) HandleText(ctx context.Context, text string) (string, error) {
	return a.HandleTextStream(ctx, text, nil)
}

func (a *Agent) HandleTextStream(ctx context.Context, text string, onDelta func(string)) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "我在。你可以随便跟我聊，也可以直接说待办，比如“明天下午三点提醒我交周报”。", nil
	}
	if reply, ok, err := a.handleCommand(ctx, text); ok || err != nil {
		return reply, err
	}
	if isGreeting(text) {
		return "你好，我在。你可以像正常聊天一样跟我说事，我会在合适的时候帮你创建、查询或更新待办。", nil
	}
	intent, fromModel := a.detectIntent(ctx, text)
	ctx = withInputIntent(ctx, intent)

	if fromModel && a.model != nil && a.model.IsConfigured() {
		if intent.TodoRelated && intent.Op == opUnknown {
			return a.runModelWithIntentTools(ctx, intent, text, onDelta)
		}
		if intent.TodoRelated || intent.Op == opChat {
			return a.runModelWithIntentTools(ctx, intent, text, onDelta)
		}
	}

	if reply, ok, err := a.ruleBasedDelete(ctx, intent, text); ok || err != nil {
		return reply, err
	}
	if reply, ok, err := a.ruleBasedTimeUpdate(ctx, intent, text); ok || err != nil {
		return reply, err
	}
	if call, ok := a.ruleBasedCall(intent, text); ok {
		res, err := a.executeTool(ctx, intent, call)
		return res.Message, err
	}
	if intent.TodoRelated && intent.Op == opUnknown {
		return "我没判断清楚你要对待办做什么。你可以说“查看待办”、 “提醒我明天九点开会”、 “删除 18b6fc” 或 “把 phison 时间改到下午五点”。", nil
	}
	if a.model == nil || !a.model.IsConfigured() {
		return "我在，不过现在模型还没配置好。明确的待办指令我仍然能处理，比如“提醒我明天上午十点开会”。", nil
	}
	return a.runModelWithIntentTools(ctx, intent, text, onDelta)
}

func (a *Agent) detectIntent(ctx context.Context, text string) (inputIntent, bool) {
	if a.model != nil && a.model.IsConfigured() {
		intent, err := a.model.ClassifyIntent(ctx, text)
		if err == nil {
			return intent, true
		}
		log.Printf("llm intent classification failed; falling back to rules: %v", err)
	}
	return analyzeIntent(text), false
}

func (a *Agent) handleCommand(ctx context.Context, text string) (string, bool, error) {
	normalized := strings.TrimSpace(text)
	lower := strings.ToLower(normalized)
	switch lower {
	case "/help", "/?", "帮助", "指令":
		return commandHelp(), true, nil
	case "/clear", "/new", "清空上下文", "清空对话", "新对话":
		if a.runtime != nil {
			if err := a.runtime.ClearSession(ctx, defaultUserID, defaultSessionID); err != nil {
				return "", true, err
			}
		}
		return "已清空当前对话上下文。下一句会从新对话开始。", true, nil
	}
	if strings.HasPrefix(lower, "/memory") {
		reply, err := a.handleMemoryCommand(ctx, normalized)
		return reply, true, err
	}
	if normalized == "查看记忆" {
		reply, err := a.listMemories(ctx, 20)
		return reply, true, err
	}
	if rest, ok := strings.CutPrefix(normalized, "添加记忆"); ok {
		reply, err := a.addMemory(ctx, strings.TrimSpace(rest))
		return reply, true, err
	}
	if rest, ok := strings.CutPrefix(normalized, "删除记忆"); ok {
		reply, err := a.deleteMemory(ctx, strings.TrimSpace(rest))
		return reply, true, err
	}
	return "", false, nil
}

func (a *Agent) handleMemoryCommand(ctx context.Context, text string) (string, error) {
	parts := strings.Fields(text)
	if len(parts) == 0 || strings.ToLower(parts[0]) != "/memory" {
		return memoryHelp(), nil
	}
	if len(parts) == 1 {
		return memoryHelp(), nil
	}
	cmd := strings.ToLower(parts[1])
	switch cmd {
	case "help", "-h", "--help":
		return memoryHelp(), nil
	case "list":
		return a.listMemories(ctx, 20)
	case "add":
		_, rest, _ := strings.Cut(text, parts[1])
		return a.addMemory(ctx, strings.TrimSpace(rest))
	case "delete", "del", "rm":
		if len(parts) < 3 {
			return "请提供要删除的记忆 ID，例如：`/memory delete abc123`。", nil
		}
		return a.deleteMemory(ctx, parts[2])
	default:
		return memoryHelp(), nil
	}
}

func commandHelp() string {
	return strings.TrimSpace(`可用指令：

- ` + "`/?`" + `、` + "`/help`" + `：查看这份帮助
- ` + "`/clear`" + `、` + "`/new`" + `：清空当前对话上下文，不删除待办和长期记忆
- ` + "`/memory list`" + `：查看长期记忆
- ` + "`/memory add <内容>`" + `：添加长期记忆
- ` + "`/memory delete <id>`" + `：删除指定记忆

待办也可以直接用自然语言：
- ` + "`提醒我明天九点开会`" + `
- ` + "`查看待办`" + `
- ` + "`把喝水相关的待办都删掉`" + `.`)
}

func memoryHelp() string {
	return strings.TrimSpace(`记忆指令：

- ` + "`/memory list`" + `：查看长期记忆
- ` + "`/memory add <内容>`" + `：添加长期记忆
- ` + "`/memory delete <id>`" + `：删除指定记忆

示例：` + "`/memory add 我喜欢简洁直接的回答`" + `.`)
}

func (a *Agent) listMemories(ctx context.Context, limit int) (string, error) {
	if a.runtime == nil || a.runtime.memorySvc == nil {
		return "记忆服务还没有启用。", nil
	}
	entries, err := a.runtime.ListMemories(ctx, limit)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "现在还没有长期记忆。", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "长期记忆 %d 条：", len(entries))
	for _, entry := range entries {
		if entry == nil || entry.Memory == nil {
			continue
		}
		fmt.Fprintf(&b, "\n- `%s` %s", shortMemoryID(entry.ID), strings.TrimSpace(entry.Memory.Memory))
		if !entry.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, "（更新：%s）", entry.UpdatedAt.Format("2006-01-02 15:04"))
		}
	}
	return b.String(), nil
}

func (a *Agent) addMemory(ctx context.Context, text string) (string, error) {
	if a.runtime == nil || a.runtime.memorySvc == nil {
		return "记忆服务还没有启用。", nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "请提供要添加的记忆内容，例如：`/memory add 我喜欢简洁直接的回答`。", nil
	}
	entry, err := a.runtime.AddMemory(ctx, text)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "已添加记忆。", nil
	}
	return fmt.Sprintf("已添加记忆：`%s` %s", shortMemoryID(entry.ID), text), nil
}

func (a *Agent) deleteMemory(ctx context.Context, idOrPrefix string) (string, error) {
	if a.runtime == nil || a.runtime.memorySvc == nil {
		return "记忆服务还没有启用。", nil
	}
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if idOrPrefix == "" {
		return "请提供要删除的记忆 ID，例如：`/memory delete abc123`。", nil
	}
	entry, err := a.runtime.DeleteMemory(ctx, idOrPrefix)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return fmt.Sprintf("没找到 ID 为 `%s` 的记忆。", idOrPrefix), nil
	}
	return fmt.Sprintf("已删除记忆：`%s` %s", shortMemoryID(entry.ID), strings.TrimSpace(entry.Memory.Memory)), nil
}

func (a *Agent) runModelWithIntentTools(ctx context.Context, intent inputIntent, text string, onDelta func(string)) (string, error) {
	allowed := allowedToolsForIntent(intent.Op)
	if intent.TodoRelated && len(allowed) == 0 {
		allowed = map[string]bool{}
	}
	if !intent.TodoRelated {
		allowed = map[string]bool{}
	}

	sessionSvc, memorySvc := a.runtimeServices()
	rt, err := newAgentRuntimeWithAllowedTools(a.model, a.tools, a.loc, systemPrompt(time.Now().In(a.loc)), sessionSvc, memorySvc, allowed, false)
	if err != nil {
		return "", err
	}
	defer rt.Close()
	return rt.Run(ctx, defaultUserID, defaultSessionID, text, onDelta)
}

func (a *Agent) runtimeServices() (trpcsession.Service, trpcmemory.Service) {
	if a == nil || a.runtime == nil {
		return nil, nil
	}
	return a.runtime.sessionSvc, a.runtime.memorySvc
}

func analyzeIntent(text string) inputIntent {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)
	intent := inputIntent{Op: opChat, TodoRelated: false, Original: text}

	switch {
	case looksLikeDelete(text):
		intent.Op = opDelete
	case looksLikeTimeUpdate(text):
		intent.Op = opUpdate
	case strings.HasPrefix(text, "完成") || strings.HasPrefix(lower, "done") || strings.HasPrefix(lower, "complete"):
		intent.Op = opComplete
	case strings.HasPrefix(text, "重新打开") || strings.HasPrefix(text, "恢复") || strings.HasPrefix(lower, "reopen"):
		intent.Op = opReopen
	case strings.Contains(text, "延后") || strings.Contains(text, "稍后提醒") || strings.Contains(text, "晚点提醒"):
		intent.Op = opSnooze
	case looksLikeList(text):
		intent.Op = opList
	case strings.Contains(text, "总结") || strings.Contains(text, "摘要"):
		intent.Op = opSummary
	case looksLikeCreate(text):
		intent.Op = opCreate
	case mentionsTodo(text):
		intent.Op = opUnknown
	default:
		return intent
	}
	intent.TodoRelated = true
	return intent
}

func normalizeModelIntent(text string, mi modelIntent) (inputIntent, error) {
	intent := inputIntent{Original: strings.TrimSpace(text)}
	op := intentOp(strings.TrimSpace(strings.ToLower(mi.Op)))
	switch op {
	case opChat:
		intent.Op = opChat
	case opCreate, opList, opUpdate, opDelete, opComplete, opReopen, opSnooze, opSummary, opUnknown:
		intent.Op = op
		intent.TodoRelated = true
	default:
		return inputIntent{}, fmt.Errorf("unknown intent op %q", mi.Op)
	}
	if mi.TodoRelated != nil {
		intent.TodoRelated = *mi.TodoRelated
	}
	if intent.Op == opChat {
		intent.TodoRelated = false
	}
	if intent.Op != opChat && !intent.TodoRelated {
		return inputIntent{}, fmt.Errorf("todo op %q marked not todo-related", intent.Op)
	}
	if mi.Confidence > 0 && mi.Confidence < 0.45 {
		return inputIntent{}, fmt.Errorf("low intent confidence %.2f", mi.Confidence)
	}
	return intent, nil
}

func parseModelIntentJSON(text string) (modelIntent, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}
	var mi modelIntent
	if err := json.Unmarshal([]byte(text), &mi); err != nil {
		return modelIntent{}, err
	}
	if strings.TrimSpace(mi.Op) == "" {
		return modelIntent{}, errors.New("missing intent op")
	}
	return mi, nil
}

func withInputIntent(ctx context.Context, intent inputIntent) context.Context {
	return context.WithValue(ctx, intentContextKey{}, intent)
}

func inputIntentFromContext(ctx context.Context) (inputIntent, bool) {
	if ctx == nil {
		return inputIntent{}, false
	}
	intent, ok := ctx.Value(intentContextKey{}).(inputIntent)
	return intent, ok
}

func (a *Agent) executeTool(ctx context.Context, intent inputIntent, call todo.ToolCall) (todo.ToolResult, error) {
	if err := validateToolCallAgainstIntent(intent, call.Name, call.Args); err != nil {
		return todo.ToolResult{}, err
	}
	res, err := a.tools.ExecuteContext(ctx, call)
	if err != nil {
		return res, err
	}
	if err := validateToolResultAgainstIntent(intent, call.Name, res); err != nil {
		return todo.ToolResult{}, err
	}
	return res, nil
}

func validateToolCallAgainstIntent(intent inputIntent, toolName string, args map[string]interface{}) error {
	if !intent.TodoRelated {
		return nil
	}
	allowed := allowedToolsForIntent(intent.Op)
	if len(allowed) == 0 {
		return fmt.Errorf("我没判断清楚要执行什么待办操作，所以不会调用工具")
	}
	if !allowed[toolName] {
		return fmt.Errorf("已阻止不符合用户意图的工具调用：用户意图是 %s，但工具是 %s", intent.Op, toolName)
	}
	if requiresID(toolName) && strings.TrimSpace(fmt.Sprint(args["id"])) == "" {
		return fmt.Errorf("这个操作需要明确的待办 ID 或先匹配到唯一待办，我不会盲目执行")
	}
	if toolName == "todo.create" && looksLikeDelete(intent.Original) {
		return fmt.Errorf("删除/取消意图不能创建待办")
	}
	return nil
}

func validateToolResultAgainstIntent(intent inputIntent, toolName string, res todo.ToolResult) error {
	if !intent.TodoRelated {
		return nil
	}
	switch intent.Op {
	case opCreate:
		if toolName == "todo.create" && res.Item == nil {
			return fmt.Errorf("创建操作没有返回新待办，已视为失败")
		}
	case opList, opSummary:
		return nil
	default:
		if res.Item == nil && len(res.Items) == 0 {
			return fmt.Errorf("工具执行后没有返回可核对的待办结果")
		}
	}
	return nil
}

func allowedToolsForIntent(op intentOp) map[string]bool {
	switch op {
	case opCreate:
		return map[string]bool{"todo.create": true}
	case opList:
		return map[string]bool{"todo.list": true}
	case opUpdate:
		return map[string]bool{"todo.list": true, "todo.update": true}
	case opDelete:
		return map[string]bool{"todo.list": true, "todo.delete": true}
	case opComplete:
		return map[string]bool{"todo.list": true, "todo.complete": true}
	case opReopen:
		return map[string]bool{"todo.list": true, "todo.reopen": true}
	case opSnooze:
		return map[string]bool{"todo.list": true, "todo.snooze": true}
	case opSummary:
		return map[string]bool{"todo.summary": true, "todo.list": true}
	default:
		return nil
	}
}

func requiresID(toolName string) bool {
	switch toolName {
	case "todo.update", "todo.delete", "todo.complete", "todo.reopen", "todo.snooze":
		return true
	default:
		return false
	}
}

func (a *Agent) ruleBasedCall(intent inputIntent, text string) (todo.ToolCall, bool) {
	args := map[string]interface{}{}
	if intent.Op == opComplete {
		id := lastToken(text)
		return todo.ToolCall{Name: "todo.complete", Args: map[string]interface{}{"id": id}}, id != ""
	}
	if intent.Op == opReopen {
		id := lastToken(text)
		return todo.ToolCall{Name: "todo.reopen", Args: map[string]interface{}{"id": id}}, id != ""
	}
	if intent.Op == opSnooze {
		id := firstID(text)
		mins := parseMinutes(text, 30)
		return todo.ToolCall{Name: "todo.snooze", Args: map[string]interface{}{"id": id, "minutes": mins}}, id != ""
	}
	if intent.Op == opList {
		if strings.Contains(text, "今天") {
			args["today"] = true
		}
		if strings.Contains(text, "逾期") || strings.Contains(text, "过期") {
			args["overdue"] = true
		}
		if strings.Contains(text, "已完成") {
			args["status"] = "done"
		} else {
			args["status"] = "open"
		}
		return todo.ToolCall{Name: "todo.list", Args: args}, true
	}
	if intent.Op == opSummary {
		return todo.ToolCall{Name: "todo.summary", Args: args}, true
	}
	if intent.Op == opCreate && looksLikeCreate(text) {
		title := text
		for _, prefix := range []string{"提醒我", "新增", "添加", "创建"} {
			title = strings.ReplaceAll(title, prefix, "")
		}
		due, cleaned, _ := todo.ParseDue(title, time.Now().In(a.loc), a.loc)
		if cleaned != "" {
			title = cleaned
		}
		title = strings.TrimSpace(strings.Trim(title, "，。"))
		if title == "" {
			return todo.ToolCall{}, false
		}
		args["title"] = title
		args["priority"] = todo.ParsePriority(text)
		if due != nil {
			args["due_at"] = due.Format("2006-01-02 15:04")
		}
		return todo.ToolCall{Name: "todo.create", Args: args}, true
	}
	return todo.ToolCall{}, false
}

func (a *Agent) ruleBasedDelete(ctx context.Context, intent inputIntent, text string) (string, bool, error) {
	if intent.Op != opDelete {
		return "", false, nil
	}
	id := firstID(text)
	if id == "" {
		if looksLikeBulk(text) {
			reply, _, err := a.deleteMatchingOpenItems(ctx, text)
			return reply, true, err
		}
		var reply string
		var ok bool
		var err error
		id, reply, ok, err = a.resolveOpenItemID(text)
		if err != nil || !ok {
			return reply, true, err
		}
	}
	res, err := a.executeTool(ctx, intent, todo.ToolCall{
		Name: "todo.delete",
		Args: map[string]interface{}{"id": id},
	})
	return res.Message, true, err
}

func (a *Agent) deleteMatchingOpenItems(ctx context.Context, text string) (string, bool, error) {
	items, err := a.svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		return "", false, err
	}
	if len(items) == 0 {
		return "现在没有未完成待办可以删除。", false, nil
	}
	keyword := a.itemKeyword(text)
	if keyword == "" {
		return "我没识别出要按什么关键词批量删除。请说清楚关键词，比如“把喝水相关的待办都删掉”。", false, nil
	}
	matches := matchItemsByKeyword(items, keyword)
	if len(matches) == 0 {
		return fmt.Sprintf("没找到包含“%s”的未完成待办。", keyword), false, nil
	}

	var deleted []todo.Item
	for _, item := range matches {
		res, err := a.executeTool(ctx, inputIntent{Op: opDelete, TodoRelated: true, Original: text}, todo.ToolCall{
			Name: "todo.delete",
			Args: map[string]interface{}{"id": item.ID},
		})
		if err != nil {
			return "", false, err
		}
		if res.Item != nil {
			deleted = append(deleted, *res.Item)
		}
	}
	return formatBulkDeleteResult(keyword, deleted), true, nil
}

func (a *Agent) ruleBasedTimeUpdate(ctx context.Context, intent inputIntent, text string) (string, bool, error) {
	if intent.Op != opUpdate {
		return "", false, nil
	}
	due, _, err := todo.ParseDue(text, time.Now().In(a.loc), a.loc)
	if err != nil || due == nil {
		return "", true, errors.New("我没识别出要改成什么时间，可以说“把时间改到明天上午九点”")
	}
	id := firstID(text)
	if id == "" {
		var reply string
		var ok bool
		id, reply, ok, err = a.resolveOpenItemID(text)
		if err != nil || !ok {
			return reply, true, err
		}
	}
	res, err := a.executeTool(ctx, intent, todo.ToolCall{
		Name: "todo.update",
		Args: map[string]interface{}{
			"id":     id,
			"due_at": due.Format("2006-01-02 15:04"),
		},
	})
	return res.Message, true, err
}

func (a *Agent) resolveOpenItemID(text string) (string, string, bool, error) {
	items, err := a.svc.List(todo.Filter{Status: todo.StatusOpen})
	if err != nil {
		return "", "", false, err
	}
	if len(items) == 0 {
		return "", "现在没有未完成待办可以处理。", false, nil
	}
	keyword := a.itemKeyword(text)
	if keyword != "" {
		matches := matchItemsByKeyword(items, keyword)
		switch len(matches) {
		case 0:
			return "", fmt.Sprintf("没找到包含“%s”的未完成待办。你可以先说“查看待办”，或者直接带上 ID。", keyword), false, nil
		case 1:
			return matches[0].ID, "", true, nil
		default:
			return "", fmt.Sprintf("找到多条包含“%s”的未完成待办。请带上 ID 再操作。", keyword), false, nil
		}
	}
	if len(items) == 1 {
		return items[0].ID, "", true, nil
	}
	return "", "你有多条未完成待办。为了避免误删或改错，请带上 ID，或者说“查看待办”。", false, nil
}

func (a *Agent) itemKeyword(text string) string {
	if _, cleaned, err := todo.ParseDue(text, time.Now().In(a.loc), a.loc); err == nil && cleaned != "" {
		text = cleaned
	}
	text = strings.ToLower(text)
	text = regexp.MustCompile(`[0-9a-f]{4,}`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`(改到|改成|调整到|调整成).*$`).ReplaceAllString(text, "")
	for _, phrase := range []string{
		"请帮我", "帮我把", "帮我", "麻烦", "给我", "请", "把", "将",
		"删除", "删掉", "取消", "移除", "撤销", "不要", "不用", "别",
		"提醒我", "提醒", "待办事项", "待办", "任务", "事项",
		"截止时间", "截至时间", "到期时间", "时间",
		"相关的", "相关", "有关的", "有关", "全部", "所有", "都",
		"这个", "这条", "一下", "的", "了", "吧",
		" ", "\t", "\r", "\n", "，", "。", ",", ".", "：", ":",
	} {
		text = strings.ReplaceAll(text, phrase, "")
	}
	return strings.TrimSpace(text)
}

func matchItemsByKeyword(items []todo.Item, keyword string) []todo.Item {
	needle := compactForMatch(keyword)
	if needle == "" {
		return nil
	}
	var matches []todo.Item
	for _, item := range items {
		title := compactForMatch(item.Title)
		if strings.Contains(title, needle) || strings.Contains(needle, title) {
			matches = append(matches, item)
		}
	}
	return matches
}

func formatBulkDeleteResult(keyword string, deleted []todo.Item) string {
	if len(deleted) == 0 {
		return fmt.Sprintf("没找到包含“%s”的未完成待办。", keyword)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "已删除 %d 条包含“%s”的待办：", len(deleted), keyword)
	for _, item := range deleted {
		fmt.Fprintf(&b, "\n- %s", item.Title)
	}
	return b.String()
}

func compactForMatch(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), ""))
}

func looksLikeDelete(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(lower, "delete") ||
		strings.Contains(lower, " delete ") ||
		strings.HasPrefix(text, "删除") ||
		strings.HasPrefix(text, "删掉") ||
		strings.HasPrefix(text, "移除") ||
		strings.HasPrefix(text, "取消") ||
		strings.Contains(text, "删掉") ||
		strings.Contains(text, "删除") ||
		strings.Contains(text, "移除") ||
		strings.Contains(text, "删除提醒") ||
		strings.Contains(text, "删除待办") ||
		strings.Contains(text, "删掉提醒") ||
		strings.Contains(text, "取消提醒") ||
		strings.Contains(text, "取消待办") ||
		strings.Contains(text, "不要提醒") ||
		strings.Contains(text, "不用提醒") ||
		strings.Contains(text, "别提醒") ||
		strings.Contains(text, "撤销提醒")
}

func looksLikeBulk(text string) bool {
	return strings.Contains(text, "都") ||
		strings.Contains(text, "全部") ||
		strings.Contains(text, "所有") ||
		strings.Contains(text, "相关") ||
		strings.Contains(text, "有关")
}

func looksLikeTimeUpdate(text string) bool {
	return (strings.Contains(text, "改") || strings.Contains(text, "调整") || strings.Contains(text, "改到") || strings.Contains(text, "改成")) &&
		(strings.Contains(text, "时间") || strings.Contains(text, "提醒") || strings.Contains(text, "到期") || strings.Contains(text, "截止") || strings.Contains(text, "截至"))
}

func looksLikeList(text string) bool {
	normalized := strings.Trim(strings.ToLower(text), " \t\r\n，。!！?")
	if normalized == "待办" || normalized == "我的待办" || normalized == "提醒" || normalized == "我的提醒" {
		return true
	}
	return strings.Contains(text, "查看") ||
		strings.Contains(text, "列表") ||
		strings.Contains(text, "列出") ||
		strings.Contains(text, "看看") ||
		strings.Contains(text, "有哪些") ||
		strings.Contains(text, "有什么")
}

func looksLikeCreate(text string) bool {
	if looksLikeDelete(text) || looksLikeTimeUpdate(text) {
		return false
	}
	return strings.Contains(text, "提醒我") ||
		strings.Contains(text, "记一下") ||
		strings.Contains(text, "记下") ||
		strings.Contains(text, "帮我记") ||
		strings.HasPrefix(text, "新增") ||
		strings.HasPrefix(text, "添加") ||
		strings.HasPrefix(text, "创建")
}

func mentionsTodo(text string) bool {
	return strings.Contains(text, "待办") ||
		strings.Contains(text, "提醒") ||
		strings.Contains(text, "任务") ||
		strings.Contains(text, "事项")
}

func systemPrompt(now time.Time) string {
	return fmt.Sprintf(`你是一个自然、温和、能正常聊天的中文待办助手。当前时间：%s。
普通聊天时自然回复，不要调用工具。用户明确表达待办意图时，使用可用工具创建、查询或更新待办。
创建待办时，title 只保留核心事项，不要包含“提醒我、帮我记一下、到时候、要、一下”等口语壳，也不要包含日期、时间、优先级；日期时间放到 due_at，优先级放到 priority。
删除、完成、重开、延后、更新这类需要 ID 的操作，如果用户没有直接给 ID，先用 todo_list 查找候选，再对匹配项调用对应工具；不要臆造 ID。
工具执行后，用自然中文简短总结结果。priority 只能是 low, normal, high, urgent。`,
		now.Format("2006-01-02 15:04"))
}

func isGreeting(text string) bool {
	normalized := strings.Trim(strings.ToLower(text), " \t\r\n，。!！?")
	switch normalized {
	case "你好", "您好", "hi", "hello", "哈喽", "嗨", "在吗", "在不在":
		return true
	default:
		return false
	}
}

type OpenAIClient struct {
	cfg   config.ModelConfig
	model trpcmodel.Model
}

func NewOpenAIClient(cfg config.ModelConfig) *OpenAIClient {
	opts := []trpcopenai.Option{}
	if cfg.BaseURL != "" {
		opts = append(opts, trpcopenai.WithBaseURL(cfg.BaseURL))
	}
	if cfg.APIKey != "" {
		opts = append(opts, trpcopenai.WithAPIKey(cfg.APIKey))
	}
	return &OpenAIClient{cfg: cfg, model: trpcopenai.New(cfg.Model, opts...)}
}

func (c *OpenAIClient) IsConfigured() bool {
	return c != nil && c.cfg.BaseURL != "" && c.cfg.APIKey != "" && c.cfg.Model != ""
}

func (c *OpenAIClient) Ping(ctx context.Context) error {
	if !c.IsConfigured() {
		return errors.New("missing model base_url/api_key/model")
	}
	ch, err := c.model.GenerateContent(ctx, &trpcmodel.Request{
		Messages: []trpcmodel.Message{trpcmodel.NewUserMessage("ping")},
		GenerationConfig: trpcmodel.GenerationConfig{
			Stream: false,
		},
	})
	if err != nil {
		return err
	}
	for resp := range ch {
		if resp != nil && resp.Error != nil {
			return resp.Error
		}
	}
	return nil
}

func (c *OpenAIClient) ClassifyIntent(ctx context.Context, text string) (inputIntent, error) {
	if !c.IsConfigured() {
		return inputIntent{}, errors.New("missing model base_url/api_key/model")
	}
	temp := 0.0
	maxTokens := 120
	system := `你是待办助手的意图分类器。只输出 JSON，不要输出解释或 Markdown。
JSON 字段：
- op: chat, create, list, update, delete, complete, reopen, snooze, summary, unknown
- todo_related: boolean
- confidence: 0 到 1 的数字
含义：
- chat: 普通聊天，不涉及待办操作
- create: 创建新待办或提醒
- list: 查询待办
- update: 修改标题、备注、优先级、时间等
- delete: 删除、取消、移除待办或提醒
- complete: 标记完成
- reopen: 重新打开
- snooze: 延后提醒
- summary: 总结待办
- unknown: 涉及待办但操作不清楚
如果用户要“删掉/删除/取消/移除”某些待办，op 必须是 delete，不要判成 list 或 create。`
	ch, err := c.model.GenerateContent(ctx, &trpcmodel.Request{
		Messages: []trpcmodel.Message{
			trpcmodel.NewSystemMessage(system),
			trpcmodel.NewUserMessage(text),
		},
		GenerationConfig: trpcmodel.GenerationConfig{
			Stream:      false,
			Temperature: &temp,
			MaxTokens:   &maxTokens,
		},
	})
	if err != nil {
		return inputIntent{}, err
	}
	var content strings.Builder
	var final string
	for resp := range ch {
		if resp == nil {
			continue
		}
		if resp.Error != nil {
			return inputIntent{}, resp.Error
		}
		for _, choice := range resp.Choices {
			if choice.Message.Content != "" {
				final = choice.Message.Content
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
			}
		}
	}
	if strings.TrimSpace(final) == "" {
		final = content.String()
	}
	mi, err := parseModelIntentJSON(final)
	if err != nil {
		return inputIntent{}, err
	}
	return normalizeModelIntent(text, mi)
}

func (c *OpenAIClient) ChatWithTools(ctx context.Context, system, user string, tools *todo.ToolExecutor) (string, error) {
	return c.ChatWithToolsStream(ctx, system, user, tools, nil)
}

func (c *OpenAIClient) ChatWithToolsStream(ctx context.Context, system, user string, tools *todo.ToolExecutor, onDelta func(string)) (string, error) {
	if !c.IsConfigured() {
		return "", errors.New("missing model base_url/api_key/model")
	}
	rt, err := newAgentRuntime(c, tools, time.Local, system, nil, nil)
	if err != nil {
		return "", err
	}
	return rt.Run(ctx, defaultUserID, defaultSessionID, user, onDelta)
}

type AgentRuntime struct {
	runner     runner.Runner
	sessionSvc trpcsession.Service
	memorySvc  trpcmemory.Service
	closers    []io.Closer
}

func NewAgentRuntime(model *OpenAIClient, tools *todo.ToolExecutor, loc *time.Location) *AgentRuntime {
	rt, err := newAgentRuntime(model, tools, loc, systemPrompt(time.Now().In(loc)), nil, nil)
	if err != nil {
		log.Printf("agent runtime sqlite session disabled: %v", err)
		return newInMemoryAgentRuntime(model, tools, loc, systemPrompt(time.Now().In(loc)))
	}
	return rt
}

func NewSQLiteAgentRuntime(model *OpenAIClient, tools *todo.ToolExecutor, loc *time.Location, dataDir string) (*AgentRuntime, error) {
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "agent-session.db"))
	if err != nil {
		return nil, err
	}
	sessionSvc, err := trpcsessiondb.NewService(db, trpcsessiondb.WithEnableAsyncPersist(true))
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	memDB, err := sql.Open("sqlite", filepath.Join(dataDir, "agent-memory.db"))
	if err != nil {
		_ = sessionSvc.Close()
		return nil, err
	}
	memorySvc, err := trpcmemorydb.NewService(memDB, trpcmemorydb.WithSoftDelete(true))
	if err != nil {
		_ = sessionSvc.Close()
		_ = memDB.Close()
		return nil, err
	}

	return newAgentRuntime(model, tools, loc, systemPrompt(time.Now().In(loc)), sessionSvc, memorySvc)
}

func newInMemoryAgentRuntime(model *OpenAIClient, tools *todo.ToolExecutor, loc *time.Location, instruction string) *AgentRuntime {
	rt, err := newAgentRuntime(model, tools, loc, instruction, nil, nil)
	if err != nil {
		panic(err)
	}
	return rt
}

func newAgentRuntime(model *OpenAIClient, tools *todo.ToolExecutor, loc *time.Location, instruction string, sessionSvc trpcsession.Service, memorySvc trpcmemory.Service) (*AgentRuntime, error) {
	return newAgentRuntimeWithAllowedTools(model, tools, loc, instruction, sessionSvc, memorySvc, nil, true)
}

func newAgentRuntimeWithAllowedTools(model *OpenAIClient, tools *todo.ToolExecutor, loc *time.Location, instruction string, sessionSvc trpcsession.Service, memorySvc trpcmemory.Service, allowed map[string]bool, closeServices bool) (*AgentRuntime, error) {
	if loc == nil {
		loc = time.Local
	}
	agentTools := todoAgentTools(tools)
	if allowed != nil {
		agentTools = filterAgentTools(agentTools, allowed)
	}
	temp := model.cfg.Temperature
	ag := llmagent.New("todo_assistant",
		llmagent.WithModel(model.model),
		llmagent.WithInstruction(instruction),
		llmagent.WithGenerationConfig(trpcmodel.GenerationConfig{
			Temperature: &temp,
			Stream:      true,
		}),
		llmagent.WithTools(agentTools),
		llmagent.WithEnableParallelTools(false),
		llmagent.WithEnableCodeExecutionResponseProcessor(false),
		llmagent.WithMaxToolIterations(4),
		llmagent.WithAddCurrentTime(true),
		llmagent.WithTimezone(loc.String()),
		llmagent.WithTimeFormat("2006-01-02 15:04"),
	)
	opts := []runner.Option{}
	var closers []io.Closer
	if sessionSvc != nil {
		opts = append(opts, runner.WithSessionService(sessionSvc))
		if closeServices {
			closers = append(closers, sessionSvc)
		}
	}
	if memorySvc != nil {
		opts = append(opts, runner.WithMemoryService(memorySvc))
		if closeServices {
			closers = append(closers, memorySvc)
		}
	}
	return &AgentRuntime{
		runner:     runner.NewRunner(agentAppName, ag, opts...),
		sessionSvc: sessionSvc,
		memorySvc:  memorySvc,
		closers:    closers,
	}, nil
}

func filterAgentTools(tools []trpctool.Tool, allowed map[string]bool) []trpctool.Tool {
	if allowed == nil {
		return tools
	}
	out := make([]trpctool.Tool, 0, len(tools))
	for _, tool := range tools {
		if named, ok := tool.(*todoTool); ok && allowed[named.todoName] {
			out = append(out, tool)
		}
	}
	return out
}

func (r *AgentRuntime) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.runner != nil {
		if err := r.runner.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, closer := range r.closers {
		if err := closer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *AgentRuntime) ClearSession(ctx context.Context, userID, sessionID string) error {
	if r == nil || r.sessionSvc == nil {
		return nil
	}
	return r.sessionSvc.DeleteSession(ctx, trpcsession.Key{
		AppName:   agentAppName,
		UserID:    userID,
		SessionID: sessionID,
	})
}

func (r *AgentRuntime) ListMemories(ctx context.Context, limit int) ([]*trpcmemory.Entry, error) {
	if r == nil || r.memorySvc == nil {
		return nil, nil
	}
	return r.memorySvc.ReadMemories(ctx, trpcmemory.UserKey{
		AppName: agentAppName,
		UserID:  defaultUserID,
	}, limit)
}

func (r *AgentRuntime) AddMemory(ctx context.Context, text string) (*trpcmemory.Entry, error) {
	if r == nil || r.memorySvc == nil {
		return nil, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	userKey := trpcmemory.UserKey{AppName: agentAppName, UserID: defaultUserID}
	if err := r.memorySvc.AddMemory(ctx, userKey, text, nil); err != nil {
		return nil, err
	}
	entries, err := r.memorySvc.ReadMemories(ctx, userKey, 100)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry != nil && entry.Memory != nil && strings.TrimSpace(entry.Memory.Memory) == text {
			return entry, nil
		}
	}
	return nil, nil
}

func (r *AgentRuntime) DeleteMemory(ctx context.Context, idOrPrefix string) (*trpcmemory.Entry, error) {
	if r == nil || r.memorySvc == nil {
		return nil, nil
	}
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if idOrPrefix == "" {
		return nil, nil
	}
	entries, err := r.ListMemories(ctx, 0)
	if err != nil {
		return nil, err
	}
	var matches []*trpcmemory.Entry
	for _, entry := range entries {
		if entry == nil || entry.ID == "" {
			continue
		}
		if entry.ID == idOrPrefix || strings.HasPrefix(entry.ID, idOrPrefix) {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("memory ID prefix %q matches %d memories; please use a longer ID", idOrPrefix, len(matches))
	}
	entry := matches[0]
	err = r.memorySvc.DeleteMemory(ctx, trpcmemory.Key{
		AppName:  agentAppName,
		UserID:   defaultUserID,
		MemoryID: entry.ID,
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func shortMemoryID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func (r *AgentRuntime) Run(ctx context.Context, userID, sessionID, text string, onDelta func(string)) (string, error) {
	ch, err := r.runner.Run(ctx, userID, sessionID, trpcmodel.NewUserMessage(text))
	if err != nil {
		return "", err
	}
	var streamed strings.Builder
	var final string
	for evt := range ch {
		if evt == nil || evt.Response == nil {
			continue
		}
		if evt.IsTerminalError() && evt.Response.Error != nil {
			return "", evt.Response.Error
		}
		for _, choice := range evt.Response.Choices {
			if choice.Delta.Content != "" {
				streamed.WriteString(choice.Delta.Content)
				if onDelta != nil {
					onDelta(streamed.String())
				}
			}
			if choice.Message.Role == trpcmodel.RoleAssistant && strings.TrimSpace(choice.Message.Content) != "" {
				final = choice.Message.Content
			}
		}
	}
	if strings.TrimSpace(final) != "" {
		return strings.TrimSpace(final), nil
	}
	return strings.TrimSpace(streamed.String()), nil
}

type todoTool struct {
	name        string
	todoName    string
	description string
	inputSchema *trpctool.Schema
	exec        *todo.ToolExecutor
}

func (t *todoTool) Declaration() *trpctool.Declaration {
	return &trpctool.Declaration{
		Name:        t.name,
		Description: t.description,
		InputSchema: t.inputSchema,
	}
}

func (t *todoTool) Call(ctx context.Context, jsonArgs []byte) (any, error) {
	args := map[string]interface{}{}
	if len(strings.TrimSpace(string(jsonArgs))) > 0 {
		if err := json.Unmarshal(jsonArgs, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments for %s: %w", t.name, err)
		}
	}
	if intent, ok := inputIntentFromContext(ctx); ok {
		if err := validateToolCallAgainstIntent(intent, t.todoName, args); err != nil {
			return nil, err
		}
	}
	res, err := t.exec.ExecuteContext(ctx, todo.ToolCall{Name: t.todoName, Args: args})
	if err != nil {
		return nil, err
	}
	if intent, ok := inputIntentFromContext(ctx); ok {
		if err := validateToolResultAgainstIntent(intent, t.todoName, res); err != nil {
			return nil, err
		}
	}
	log.Printf("agent tool call: name=%s args=%s result=%s", t.name, string(jsonArgs), res.Message)
	return res, nil
}

func todoAgentTools(exec *todo.ToolExecutor) []trpctool.Tool {
	return []trpctool.Tool{
		newTodoTool(exec, "todo_create", "创建一个新的待办事项。", "todo.create", objectSchema(
			properties(
				prop("title", stringSchema("待办标题")),
				prop("note", stringSchema("备注")),
				prop("chat_id", stringSchema("来源会话 ID，通常由系统自动注入")),
				prop("priority", enumSchema("优先级", "low", "normal", "high", "urgent")),
				prop("due_at", stringSchema("到期时间，自然语言或 yyyy-mm-dd hh:mm")),
			),
			"title",
		)),
		newTodoTool(exec, "todo_list", "查询待办列表。", "todo.list", objectSchema(
			properties(
				prop("status", enumSchema("状态", "open", "done")),
				prop("today", boolSchema("只看今天到期")),
				prop("overdue", boolSchema("只看逾期")),
				prop("priority", enumSchema("优先级", "low", "normal", "high", "urgent")),
			),
		)),
		newTodoTool(exec, "todo_update", "更新待办事项。", "todo.update", objectSchema(
			properties(
				prop("id", stringSchema("待办 ID 或前缀")),
				prop("title", stringSchema("新标题")),
				prop("note", stringSchema("新备注，空字符串表示清空")),
				prop("chat_id", stringSchema("新的来源会话 ID，通常不需要修改")),
				prop("priority", enumSchema("优先级", "low", "normal", "high", "urgent")),
				prop("due_at", stringSchema("新到期时间，空字符串表示清空")),
			),
			"id",
		)),
		newTodoTool(exec, "todo_complete", "将待办标记为完成。", "todo.complete", idSchema()),
		newTodoTool(exec, "todo_reopen", "重新打开已完成的待办。", "todo.reopen", idSchema()),
		newTodoTool(exec, "todo_delete", "删除待办。", "todo.delete", idSchema()),
		newTodoTool(exec, "todo_snooze", "延后待办提醒时间。", "todo.snooze", objectSchema(
			properties(
				prop("id", stringSchema("待办 ID 或前缀")),
				prop("minutes", numberSchema("延后分钟数")),
			),
			"id",
		)),
		newTodoTool(exec, "todo_summary", "汇总当前未完成待办。", "todo.summary", objectSchema(properties())),
	}
}

func newTodoTool(exec *todo.ToolExecutor, name, description, todoName string, schema *trpctool.Schema) *todoTool {
	return &todoTool{name: name, description: description, todoName: todoName, inputSchema: schema, exec: exec}
}

func idSchema() *trpctool.Schema {
	return objectSchema(properties(prop("id", stringSchema("待办 ID 或前缀"))), "id")
}

func objectSchema(props map[string]*trpctool.Schema, required ...string) *trpctool.Schema {
	return &trpctool.Schema{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: false,
	}
}

func properties(entries ...[2]any) map[string]*trpctool.Schema {
	out := map[string]*trpctool.Schema{}
	for _, entry := range entries {
		out[entry[0].(string)] = entry[1].(*trpctool.Schema)
	}
	return out
}

func prop(name string, schema *trpctool.Schema) [2]any {
	return [2]any{name, schema}
}

func stringSchema(description string) *trpctool.Schema {
	return &trpctool.Schema{Type: "string", Description: description}
}

func boolSchema(description string) *trpctool.Schema {
	return &trpctool.Schema{Type: "boolean", Description: description}
}

func numberSchema(description string) *trpctool.Schema {
	return &trpctool.Schema{Type: "integer", Description: description}
}

func enumSchema(description string, values ...string) *trpctool.Schema {
	enum := make([]any, len(values))
	for i, value := range values {
		enum[i] = value
	}
	return &trpctool.Schema{Type: "string", Description: description, Enum: enum}
}

func lastToken(text string) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return ""
	}
	return strings.Trim(parts[len(parts)-1], "，。")
}

func firstID(text string) string {
	re := regexp.MustCompile(`[0-9a-fA-F]{4,}`)
	return re.FindString(text)
}

func parseMinutes(text string, def int) int {
	re := regexp.MustCompile(`(\d+)\s*(分钟|分)`)
	if m := re.FindStringSubmatch(text); len(m) > 0 {
		var n int
		fmt.Sscanf(m[1], "%d", &n)
		return n
	}
	re = regexp.MustCompile(`(\d+)\s*(小时)`)
	if m := re.FindStringSubmatch(text); len(m) > 0 {
		var n int
		fmt.Sscanf(m[1], "%d", &n)
		return n * 60
	}
	return def
}
