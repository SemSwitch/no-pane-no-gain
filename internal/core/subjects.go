package core

import "fmt"

const (
	SubjectRegistryOnline  = "nexis.registry.online"
	SubjectRegistryOffline = "nexis.registry.offline"
	SubjectAgentState      = "nexis.agent.state"
	SubjectLLM             = "nexis.llm"
	SubjectTool            = "nexis.tool"
	SubjectTaskTrigger     = "nexis.task.trigger"
	SubjectSystemError     = "nexis.system.error"
)

func SubjectForEvent(event Event) string {
	switch event.Kind {
	case EventRegistryOnline:
		return SubjectRegistryOnline
	case EventRegistryOffline:
		return SubjectRegistryOffline
	case EventAgentWaiting, EventAgentBusy, EventAgentTooling, EventAgentDone:
		return fmt.Sprintf("%s.%s", SubjectAgentState, event.Role)
	case EventAgentError:
		if event.Role != "" {
			return fmt.Sprintf("%s.%s", SubjectSystemError, event.Role)
		}
		return SubjectSystemError
	case EventLLMRequest:
		return fmt.Sprintf("%s.%s.%s.request", SubjectLLM, event.Provider, event.Role)
	case EventLLMResponse:
		return fmt.Sprintf("%s.%s.%s.response", SubjectLLM, event.Provider, event.Role)
	case EventToolCall:
		return fmt.Sprintf("%s.%s.%s.call", SubjectTool, event.Provider, event.Role)
	case EventToolResult:
		return fmt.Sprintf("%s.%s.%s.result", SubjectTool, event.Provider, event.Role)
	default:
		if event.Role != "" {
			return fmt.Sprintf("%s.%s", SubjectSystemError, event.Role)
		}
		return SubjectSystemError
	}
}

func StreamSubjects() []string {
	return []string{
		SubjectRegistryOnline,
		SubjectRegistryOffline,
		SubjectAgentState + ".>",
		SubjectLLM + ".>",
		SubjectTool + ".>",
		SubjectTaskTrigger,
		SubjectSystemError + ".>",
	}
}
