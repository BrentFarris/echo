package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/tools"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const inlineCodePromptEventName = "echo:inline-code:event"

type InlineCodePromptRequest struct {
	RequestID        string `json:"requestId,omitempty"`
	FilePath         string `json:"filePath"`
	Prompt           string `json:"prompt"`
	CursorToken      string `json:"cursorToken"`
	CursorLineText   string `json:"cursorLineText"`
	FocusSubstring   string `json:"focusSubstring"`
	ContextSubstring string `json:"contextSubstring"`
	SelectedText     string `json:"selectedText,omitempty"`
}

type InlineCodePromptResponse struct {
	Content       string             `json:"content,omitempty"`
	ToolCalls     []ChatToolActivity `json:"toolCalls,omitempty"`
	AffectedPaths []string           `json:"affectedPaths,omitempty"`
}

type InlineCodePromptEvent struct {
	WorkspaceID   string            `json:"workspaceId"`
	RequestID     string            `json:"requestId,omitempty"`
	FilePath      string            `json:"filePath"`
	Type          string            `json:"type"`
	Content       string            `json:"content,omitempty"`
	ToolCall      *ChatToolActivity `json:"toolCall,omitempty"`
	AffectedPaths []string          `json:"affectedPaths,omitempty"`
	Error         string            `json:"error,omitempty"`
	FinishReason  string            `json:"finishReason,omitempty"`
}

func (s *SystemService) SubmitInlineCodePrompt(workspaceID string, request InlineCodePromptRequest) (InlineCodePromptResponse, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.FilePath = strings.TrimSpace(request.FilePath)
	request.Prompt = strings.TrimSpace(request.Prompt)
	eventBase := InlineCodePromptEvent{
		WorkspaceID: workspaceID,
		RequestID:   request.RequestID,
		FilePath:    request.FilePath,
	}
	fail := func(err error) (InlineCodePromptResponse, error) {
		s.emitInlineCodePromptEvent(InlineCodePromptEvent{
			WorkspaceID: eventBase.WorkspaceID,
			RequestID:   eventBase.RequestID,
			FilePath:    eventBase.FilePath,
			Type:        "error",
			Error:       err.Error(),
		})
		return InlineCodePromptResponse{}, err
	}
	if request.Prompt == "" {
		return fail(fmt.Errorf("prompt is required"))
	}
	if request.FilePath == "" {
		return fail(fmt.Errorf("file path is required"))
	}

	workspace, settings, err := s.workspaceAndSettingsFor(workspaceID, llm.InteractionInlineCode)
	if err != nil {
		return fail(err)
	}
	resolved, err := resolveWorkspaceServicePath(workspace, request.FilePath)
	if err != nil {
		return fail(err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fail(fmt.Errorf("file was not found"))
	}
	if !info.Mode().IsRegular() {
		return fail(fmt.Errorf("path is not a regular file"))
	}

	client, err := llm.NewClient(settings)
	if err != nil {
		return fail(err)
	}

	messages := []llm.Message{
		inlineCodeSystemMessage(workspace, workspaceSkillCandidates(context.Background(), workspace, request.Prompt+"\n"+request.FilePath)),
		{
			Role:    llm.RoleUser,
			Content: inlineCodeUserPrompt(request),
		},
	}
	currentUser := messages[1]
	toolSchema := tools.LLMSchema()
	var output InlineCodePromptResponse
	affected := map[string]bool{}
	recoverableToolCalls := make(map[string]bool)
	forcedCompactions := 0
	skillCheckpointPending := false
	skillCheckpointReminders := 0

	for {
		preflightPolicy := contextCompactionPolicy{CurrentUser: currentUser}
		if contextNeedsCompaction(settings, messages, toolSchema) &&
			contextHasCompressibleStale(settings, messages, preflightPolicy) {
			s.emitInlineCodePromptEvent(InlineCodePromptEvent{
				WorkspaceID: eventBase.WorkspaceID,
				RequestID:   eventBase.RequestID,
				FilePath:    eventBase.FilePath,
				Type:        "compacting",
				Content:     "Compacting stale context while preserving the current inline request and recent work.",
			})
			compaction, compactErr := compactContextIfNeeded(context.Background(), client, settings, messages, toolSchema, preflightPolicy)
			if compactErr == nil && compaction.Compacted {
				messages = compaction.Messages
				s.emitInlineCodeCompactionResult(eventBase, compaction)
			}
		}

		chatRequest, err := llm.NewChatRequest(settings, messages, llm.WithTools(toolSchema), llm.WithToolChoice("auto"))
		if err != nil {
			return fail(err)
		}
		publishResponse := !skillCheckpointPending
		result := s.streamInlineCodeResponse(context.Background(), client, chatRequest, eventBase, publishResponse)
		if result.err != nil {
			if llm.IsContextLengthExceeded(result.err) {
				if recovery, ok := recoverToolResultContext(messages, recoverableToolCalls); ok {
					messages = recovery.Messages
					s.markInlineToolContextTooLarge(&output, eventBase, recovery)
					continue
				}
				if forcedCompactions >= 2 {
					return fail(fmt.Errorf("Echo could not free enough context while preserving the system message, original inline request, and recent agent state"))
				}
				var compaction contextCompactionResult
				var compactErr error
				for forcedCompactions < 2 {
					forcedCompactions++
					s.emitInlineCodePromptEvent(InlineCodePromptEvent{
						WorkspaceID: eventBase.WorkspaceID,
						RequestID:   eventBase.RequestID,
						FilePath:    eventBase.FilePath,
						Type:        "compacting",
						Content:     "The provider rejected the request for context length, so Echo is compacting stale inline-agent history.",
					})
					compaction, compactErr = compactContextIfNeeded(context.Background(), client, settings, messages, toolSchema, contextCompactionPolicy{
						CurrentUser:    currentUser,
						Force:          true,
						Aggressiveness: forcedCompactions,
					})
					if compactErr == nil {
						break
					}
				}
				if compactErr != nil {
					return fail(fmt.Errorf("Echo could not compact the inline context safely: %w", compactErr))
				}
				messages = compaction.Messages
				s.emitInlineCodeCompactionResult(eventBase, compaction)
				continue
			}
			return fail(errors.New(userFacingLLMError(result.err)))
		}
		if !result.finished {
			return fail(fmt.Errorf("inline code prompt stream ended before completion"))
		}
		toolCalls := s.normalizeToolCalls(result.toolCalls)
		if err := finishReasonError(result.finishReason, len(toolCalls) > 0); err != nil {
			return fail(err)
		}
		forcedCompactions = 0

		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: result.content, ToolCalls: toolCalls})
		if len(toolCalls) == 0 {
			if skillCheckpointPending && skillCheckpointReminders < workspaceSkillMaxReminders {
				skillCheckpointReminders++
				messages = append(messages, llm.Message{
					Role:    llm.RoleUser,
					Content: workspaceSkillCheckpointPrompt(false),
				})
				continue
			}
			output.Content = strings.TrimSpace(result.content)
			if skillCheckpointPending {
				if output.Content != "" {
					output.Content += "\n\n"
				}
				output.Content += workspaceSkillCheckpointWarning()
			}
			output.AffectedPaths = sortedAffectedInlinePaths(affected)
			s.emitInlineCodePromptEvent(InlineCodePromptEvent{
				WorkspaceID:   eventBase.WorkspaceID,
				RequestID:     eventBase.RequestID,
				FilePath:      eventBase.FilePath,
				Type:          "complete",
				Content:       output.Content,
				AffectedPaths: output.AffectedPaths,
				FinishReason:  result.finishReason,
			})
			return output, nil
		}

		for _, call := range toolCalls {
			if call.ID == "" {
				call.ID = s.nextChatID("call")
			}
			s.emitInlineCodeToolCallEvent(eventBase, call, "running", "", "")
			execution := s.executeInlineCodeToolCall(workspace, settings, eventBase, call)
			recoverableToolCalls[call.ID] = true
			s.emitInlineCodePromptEvent(InlineCodePromptEvent{
				WorkspaceID: eventBase.WorkspaceID,
				RequestID:   eventBase.RequestID,
				FilePath:    eventBase.FilePath,
				Type:        "tool_call",
				ToolCall:    &execution.Activity,
			})
			output.ToolCalls = append(output.ToolCalls, execution.Activity)
			for _, changedPath := range execution.ChangedPaths {
				affected[changedPath] = true
			}
			messages = append(messages, execution.Message)
			if len(execution.ChangedPaths) > 0 {
				skillCheckpointPending = true
				skillCheckpointReminders = 0
			}
			if execution.SkillCheckpoint {
				skillCheckpointPending = false
			}
		}
	}

}

type inlineCodeStreamResult struct {
	content      string
	toolCalls    []llm.ToolCall
	finished     bool
	finishReason string
	err          error
}

func (s *SystemService) streamInlineCodeResponse(ctx context.Context, client *llm.Client, request llm.ChatRequest, eventBase InlineCodePromptEvent, publish bool) inlineCodeStreamResult {
	stream := client.StreamChat(ctx, request)
	content := strings.Builder{}
	inlineParser := inlineToolCallStreamParser{}
	toolCalls := make(map[int]llm.ToolCall)
	finished := false
	finishReason := ""
	nextInlineToolIndex := inlineToolCallIndexBase

	recordInlineToolCalls := func(calls []llm.ToolCall) {
		for _, call := range calls {
			call = s.normalizeToolCall(call)
			toolCalls[nextInlineToolIndex] = call
			nextInlineToolIndex++
			s.emitInlineCodeToolCallEvent(eventBase, call, "streaming", "", "")
		}
	}
	appendContent := func(text string) {
		if text == "" {
			return
		}
		content.WriteString(text)
		if !publish {
			return
		}
		s.emitInlineCodePromptEvent(InlineCodePromptEvent{
			WorkspaceID: eventBase.WorkspaceID,
			RequestID:   eventBase.RequestID,
			FilePath:    eventBase.FilePath,
			Type:        "token",
			Content:     text,
		})
	}
	flushInlineParser := func() {
		parsed := inlineParser.Flush()
		recordInlineToolCalls(parsed.ToolCalls)
		appendContent(parsed.Text)
	}

	for event := range stream.Events {
		switch event.Type {
		case llm.EventToken:
			parsed := inlineParser.Consume(event.Content)
			recordInlineToolCalls(parsed.ToolCalls)
			appendContent(parsed.Text)
		case llm.EventToolCall:
			if event.ToolCall != nil {
				call := mergeToolDelta(toolCalls[event.ToolCall.Index], *event.ToolCall)
				toolCalls[event.ToolCall.Index] = call
				s.emitInlineCodeToolCallEvent(eventBase, call, "streaming", "", "")
			}
		case llm.EventComplete:
			finished = true
			finishReason = event.FinishReason
		case llm.EventCanceled:
			return inlineCodeStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason}
		case llm.EventError:
			return inlineCodeStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason, err: errors.New(event.Error)}
		}
	}

	if err := ctx.Err(); err != nil {
		return inlineCodeStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: false, finishReason: finishReason}
	}
	flushInlineParser()
	return inlineCodeStreamResult{content: content.String(), toolCalls: orderedToolCalls(toolCalls), finished: finished, finishReason: finishReason}
}

func inlineCodeSystemMessage(workspace Workspace, skillCandidates []tools.WorkspaceSkillSummary) llm.Message {
	return llm.Message{
		Role: llm.RoleSystem,
		Content: workspaceSystemPrompt(
			workspaceSkillsPrompt("You are Echo's inline code assistant. Help with the user's prompt at the current editor cursor. "+
				contextCheckpointSystemGuidance+" "+
				"Use available workspace tools when you need to inspect or edit files. "+
				"When the user mentions @path, treat it as a labeled workspace file reference like <folder-label>/path and read it before relying on its contents. "+
				"When you need to find code but do not know the target file, prefer filesystem_search_workspace before shell commands. "+
				"When locating symbols, strings, or code blocks in a known file, prefer filesystem_search_text before reading the whole file. "+
				"When a search result gives a useful line number, read nearby code with filesystem_read_text aroundLine; copy the result's line value and avoid reading whole source files unless the entire file is genuinely needed. "+
				"Use lsp_query for definitions, references, hover info, document symbols, and member/completion candidates once you know the file and cursor position. "+
				"If you fully handled the request by editing files and have nothing useful to show inline, return an empty final message. "+
				"Otherwise keep the inline response concise and directly relevant to the cursor context.",
				skillCandidates,
				true,
			),
			workspace,
		),
	}
}

func inlineCodeUserPrompt(request InlineCodePromptRequest) string {
	selected := ""
	if strings.TrimSpace(request.SelectedText) != "" {
		selected = fmt.Sprintf("\n\nSelected text. If present, this is the primary target:\n```text\n%s\n```", request.SelectedText)
	}
	cursorTarget := ""
	if strings.TrimSpace(request.CursorToken) != "" || strings.TrimSpace(request.CursorLineText) != "" {
		cursorTarget = fmt.Sprintf("\n\nCursor target. If no text is selected, treat this token and source text as the primary target:\nToken: %s\nSource text:\n```text\n%s\n```", request.CursorToken, request.CursorLineText)
	}
	return fmt.Sprintf("Inline code prompt:\n\nFile: %s\nThe focused cursor substring below is copied from the file at the editor location. Treat selected text first, then the cursor target, then the focused substring as the target for the user's prompt. Use the broader context substring only for nearby context, and use workspace search/read tools to locate exact regions when needed; do not rely on line numbers.%s%s\n\nUser prompt:\n%s\n\nFocused cursor substring:\n```text\n%s\n```\n\nContext substring:\n```text\n%s\n```",
		request.FilePath,
		selected,
		cursorTarget,
		request.Prompt,
		request.FocusSubstring,
		request.ContextSubstring,
	)
}

func inlineCodeAssistantContentAndToolCalls(message llm.Message) (string, []llm.ToolCall) {
	content := message.Content
	toolCalls := append([]llm.ToolCall(nil), message.ToolCalls...)

	parser := inlineToolCallStreamParser{}
	parsed := parser.Consume(content)
	flushed := parser.Flush()
	content = parsed.Text + flushed.Text
	toolCalls = append(toolCalls, parsed.ToolCalls...)
	toolCalls = append(toolCalls, flushed.ToolCalls...)
	return content, toolCalls
}

type inlineCodeToolCallExecution struct {
	Activity        ChatToolActivity
	Message         llm.Message
	ChangedPaths    []string
	SkillCheckpoint bool
}

func (s *SystemService) executeInlineCodeToolCall(workspace Workspace, settings llm.Settings, eventBase InlineCodePromptEvent, call llm.ToolCall) inlineCodeToolCallExecution {
	execution := s.executeTrackedToolCall(context.Background(), workspace, settings, call, WorkspaceChangeSource{
		Type:      "inline",
		RequestID: eventBase.RequestID,
	}, nil)
	result := execution.Result

	data, err := json.Marshal(result)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error","message":%q}}`, call.Function.Name, err.Error()))
	}
	status := "complete"
	errorText := ""
	if !result.Success {
		status = "error"
		if result.Error != nil {
			errorText = result.Error.Message
		}
	}
	activity := ChatToolActivity{
		ID:        call.ID,
		Name:      call.Function.Name,
		Arguments: call.Function.Arguments,
		Status:    status,
		Result:    string(data),
		Error:     errorText,
	}

	return inlineCodeToolCallExecution{
		Activity: activity,
		Message: llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: call.ID,
			Content:    string(data),
		},
		ChangedPaths:    affectedPathsFromChanges(execution.Changes),
		SkillCheckpoint: workspaceSkillCheckpointCompleted(call, result),
	}
}

func (s *SystemService) emitInlineCodeToolCallEvent(eventBase InlineCodePromptEvent, call llm.ToolCall, status string, result string, errorText string) {
	activity := ChatToolActivity{
		ID:        call.ID,
		Name:      call.Function.Name,
		Arguments: call.Function.Arguments,
		Status:    status,
		Result:    result,
		Error:     errorText,
	}
	s.emitInlineCodePromptEvent(InlineCodePromptEvent{
		WorkspaceID: eventBase.WorkspaceID,
		RequestID:   eventBase.RequestID,
		FilePath:    eventBase.FilePath,
		Type:        "tool_call",
		ToolCall:    &activity,
	})
}

func (s *SystemService) markInlineToolContextTooLarge(output *InlineCodePromptResponse, eventBase InlineCodePromptEvent, recovery toolContextRecovery) {
	activity := ChatToolActivity{
		ID:        recovery.Call.ID,
		Name:      recovery.Call.Function.Name,
		Arguments: recovery.Call.Function.Arguments,
		Status:    "error",
		Result:    recovery.ResultMessage.Content,
		Error:     toolResultContextErrorText,
	}
	for index := range output.ToolCalls {
		if output.ToolCalls[index].ID == activity.ID {
			output.ToolCalls[index] = activity
			s.emitInlineCodePromptEvent(InlineCodePromptEvent{
				WorkspaceID: eventBase.WorkspaceID,
				RequestID:   eventBase.RequestID,
				FilePath:    eventBase.FilePath,
				Type:        "tool_call",
				ToolCall:    &activity,
			})
			return
		}
	}
	output.ToolCalls = append(output.ToolCalls, activity)
	s.emitInlineCodePromptEvent(InlineCodePromptEvent{
		WorkspaceID: eventBase.WorkspaceID,
		RequestID:   eventBase.RequestID,
		FilePath:    eventBase.FilePath,
		Type:        "tool_call",
		ToolCall:    &activity,
	})
}

func (s *SystemService) emitInlineCodeCompactionResult(eventBase InlineCodePromptEvent, result contextCompactionResult) {
	content := fmt.Sprintf(
		"Context compacted from approximately %d to %d tokens; %d stale messages were replaced.",
		result.BeforeTokens,
		result.AfterTokens,
		result.RemovedMessages,
	)
	if result.UsedFallback && result.Warning != "" {
		content += " " + result.Warning
	}
	s.emitInlineCodePromptEvent(InlineCodePromptEvent{
		WorkspaceID: eventBase.WorkspaceID,
		RequestID:   eventBase.RequestID,
		FilePath:    eventBase.FilePath,
		Type:        "compacted",
		Content:     content,
	})
}

func (s *SystemService) emitInlineCodePromptEvent(event InlineCodePromptEvent) {
	s.emitRuntimeEvent(inlineCodePromptEventName, event)
	if s.inlineCodeEventSink != nil {
		s.inlineCodeEventSink(event)
	}
	if s.ctx != nil {
		runtime.EventsEmit(s.ctx, inlineCodePromptEventName, event)
	}
}

func sortedAffectedInlinePaths(paths map[string]bool) []string {
	if len(paths) == 0 {
		return nil
	}
	output := make([]string, 0, len(paths))
	for path := range paths {
		output = append(output, path)
	}
	sort.Strings(output)
	return output
}
