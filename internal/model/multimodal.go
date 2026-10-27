package model

import (
	"encoding/json"
)

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *MediaURL `json:"image_url,omitempty"`
	VideoURL *MediaURL `json:"video_url,omitempty"`
	File     *FileData `json:"file,omitempty"`
}
type MediaURL struct {
	URL string `json:"url"`
}
type FileData struct {
	Filename string `json:"filename"`
	Data     string `json:"file_data"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	if len(m.Parts) == 0 {
		return json.Marshal(plain(m))
	}
	parts := make([]ContentPart, 0, len(m.Parts)+1)
	if m.Content != "" {
		parts = append(parts, ContentPart{Type: "text", Text: m.Content})
	}
	parts = append(parts, m.Parts...)
	return json.Marshal(struct {
		plain
		Content []ContentPart `json:"content"`
	}{plain(m), parts})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type plain Message
	var value struct {
		*plain
		Content json.RawMessage `json:"content"`
	}
	*m = Message{}
	value.plain = (*plain)(m)
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if len(value.Content) == 0 || string(value.Content) == "null" {
		return nil
	}
	if value.Content[0] == '"' {
		return json.Unmarshal(value.Content, &m.Content)
	}
	var parts []ContentPart
	if err := json.Unmarshal(value.Content, &parts); err != nil {
		return err
	}
	for _, part := range parts {
		if part.Type == "text" {
			m.Content += part.Text
		} else {
			m.Parts = append(m.Parts, part)
		}
	}
	return nil
}

// Binary data URLs are not text tokens. Summaries use attachment descriptions;
// recent turns retain the original media, and the stored transcript is unchanged.
func (m Message) Summary() Message {
	for _, part := range m.Parts {
		if part.Type == "text" {
			m.Content += "\n" + part.Text
		} else {
			m.Content += "\n[Attached " + part.Type + "]"
		}
	}
	m.Parts = nil
	return m
}
