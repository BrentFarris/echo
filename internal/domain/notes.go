package domain

// NoteMention ranges are half-open UTF-16 offsets in Markdown (browser offsets).
// Labels are historical; identity never depends on a mutable bot/machine name.
type NoteMention struct {
	NodeID string `json:"nodeId"`
	BotID  string `json:"botId"`
	Label  string `json:"label"`
	Start  int    `json:"start"`
	End    int    `json:"end"`
}

type Note struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Markdown  string        `json:"markdown"`
	Mentions  []NoteMention `json:"mentions"`
	Revision  int64         `json:"revision"`
	CreatedAt string        `json:"createdAt"`
	UpdatedAt string        `json:"updatedAt"`
}
