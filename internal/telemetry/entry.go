package telemetry

import "encoding/json"

type Entry struct {
	Raw        json.RawMessage `json:"-"`
	Role       string          `json:"role,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Type       string          `json:"type,omitempty"`
	Content    []ContentItem   `json:"content,omitempty"`
}

type ContentItem struct {
	Type      string `json:"type,omitempty"`
	ID        string `json:"id,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
}

func ParseLine(line []byte) (Entry, error) {
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		return Entry{}, err
	}
	entry.Raw = append(entry.Raw[:0], line...)
	return entry, nil
}

func (e Entry) HasTopLevelError() bool {
	return e.Type == "error"
}

func (e Entry) HasContentError() bool {
	for _, item := range e.Content {
		if item.Type == "error" {
			return true
		}
	}
	return false
}

func (e Entry) HasError() bool {
	return e.HasTopLevelError() || e.HasContentError()
}
