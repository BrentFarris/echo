package jira

import (
	"encoding/json"
	"fmt"
	"strings"
)

const jiraEnvAuth = "ATLASSIAN_AUTH_TOKEN"

// SearchResponse maps Jira /rest/api/3/search response.
type SearchResponse struct {
	Expand     string   `json:"expand,omitempty"`
	Issues     []Ticket `json:"issues,omitempty"`
	MaxResults int      `json:"maxResults,omitempty"`
	StartAt    int      `json:"startAt,omitempty"`
	Total      int      `json:"total,omitempty"`
}

// Ticket is a Jira issue.
type Ticket struct {
	Id     string       `json:"id,omitempty"`
	Key    string       `json:"key,omitempty"`
	Self   string       `json:"self,omitempty"`
	Expand string       `json:"expand,omitempty"`
	Fields TicketFields `json:"fields,omitempty"`
}

// TicketFields contains the issue detail fields.
type TicketFields struct {
	Comment         TicketComments  `json:"comment,omitempty"`
	Components      []TicketComponent `json:"components,omitempty"`
	Created         string          `json:"created,omitempty"`
	Assignee        *TicketAccount  `json:"assignee,omitempty"`
	Creator         *TicketAccount  `json:"creator,omitempty"`
	Description     ADFNode         `json:"description,omitempty"`
	Issuelinks      []LinkedTicket  `json:"issuelinks,omitempty"`
	Priority        *TicketPriority `json:"priority,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	FixVersions     []FixVersion    `json:"fixVersions,omitempty"`
	Status          TicketStatus    `json:"status,omitempty"`
	ResolutionDate  string          `json:"resolutiondate,omitempty"`
	DescriptionText string          `json:"descriptionText,omitempty"`
}

// TicketComponent is a project component.
type TicketComponent struct {
	Id   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Self string `json:"self,omitempty"`
}

// FixVersion is a version that fixes the issue.
type FixVersion struct {
	Id          string `json:"id,omitempty"`
	Self        string `json:"self,omitempty"`
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
	Released    bool   `json:"released,omitempty"`
	ReleaseDate string `json:"releaseDate,omitempty"`
}

// TicketStatus is the issue status.
type TicketStatus struct {
	Name string `json:"name"`
}

// TicketPriority is the issue priority.
type TicketPriority struct {
	Id      string `json:"id,omitempty"`
	IconUrl string `json:"iconUrl,omitempty"`
	Name    string `json:"name,omitempty"`
	Self    string `json:"self,omitempty"`
}

// TicketComments wraps comment data.
type TicketComments struct {
	Comments   []TicketComment `json:"comments,omitempty"`
	MaxResults int             `json:"maxResults,omitempty"`
	Self       string          `json:"self,omitempty"`
	StartAt    int             `json:"startAt,omitempty"`
	Total      int             `json:"total,omitempty"`
}

// TicketComment is a single comment.
type TicketComment struct {
	Author       *TicketAccount `json:"author,omitempty"`
	Body         ADFNode        `json:"body,omitempty"`
	Created      string         `json:"created,omitempty"`
	Id           string         `json:"id,omitempty"`
	JsdPublic    bool           `json:"jsdPublic,omitempty"`
	Self         string         `json:"self,omitempty"`
	UpdateAuthor *TicketAccount `json:"updateAuthor,omitempty"`
	Updated      string         `json:"updated,omitempty"`
	BodyText     string         `json:"bodyText,omitempty"`
}

// TicketAccount is a Jira user/account.
type TicketAccount struct {
	AccountId    string     `json:"accountId,omitempty"`
	AccountType  string     `json:"accountType,omitempty"`
	Active       bool       `json:"active,omitempty"`
	AvatarUrls   AvatarUrls `json:"avatarUrls,omitempty"`
	DisplayName  string     `json:"displayName,omitempty"`
	EmailAddress string     `json:"emailAddress,omitempty"`
	Self         string     `json:"self,omitempty"`
	TimeZone     string     `json:"timeZone,omitempty"`
}

// AvatarUrls is avatar size mappings.
type AvatarUrls struct {
	Size16x16 string `json:"16x16,omitempty"`
	Size24x24 string `json:"24x24,omitempty"`
	Size32x32 string `json:"32x32,omitempty"`
	Size48x48 string `json:"48x48,omitempty"`
}

// LinkedTicket is an issue link.
type LinkedTicket struct {
	Id          string           `json:"id,omitempty"`
	InwardIssue Ticket           `json:"inwardIssue,omitempty"`
	Self        string           `json:"self,omitempty"`
	Type        LinkedTicketType `json:"type,omitempty"`
}

// LinkedTicketType describes the link direction.
type LinkedTicketType struct {
	Id      string `json:"id,omitempty"`
	Inward  string `json:"inward,omitempty"`
	Name    string `json:"name,omitempty"`
	Outward string `json:"outward,omitempty"`
	Self    string `json:"self,omitempty"`
}

// ADFNode is a node in Atlassian Document Format.
type ADFNode struct {
	Type    string    `json:"type,omitempty"`
	Text    string    `json:"text,omitempty"`
	Content []ADFNode `json:"content,omitempty"`
	Marks   []ADFMark `json:"marks,omitempty"`
	Version float64   `json:"version,omitempty"`
}

// ADFMark is an inline mark in ADF.
type ADFMark struct {
	Type  string       `json:"type,omitempty"`
	Attrs ADFMarkAttrs `json:"attrs,omitempty"`
}

// ADFMarkAttrs holds link href attributes.
type ADFMarkAttrs struct {
	Href string `json:"href,omitempty"`
}

// adfToText converts an ADF node tree to plain text with basic formatting.
func adfToText(node ADFNode) string {
	return extractADFNodeText(node, "")
}

func extractADFNodeText(node ADFNode, indent string) string {
	var b strings.Builder
	switch node.Type {
	case "doc":
		for _, child := range node.Content {
			b.WriteString(extractADFNodeText(child, indent))
		}
	case "paragraph", "heading":
		for _, child := range node.Content {
			b.WriteString(extractADFNodeText(child, indent))
		}
		b.WriteString("\n")
	case "text":
		text := node.Text
		for _, mark := range node.Marks {
			switch mark.Type {
			case "strong":
				text = "*" + text + "*"
			case "em":
				text = "_" + text + "_"
			case "link":
				text = fmt.Sprintf("%s (%s)", text, mark.Attrs.Href)
			}
		}
		b.WriteString(text)
	case "bulletList":
		for _, li := range node.Content {
			b.WriteString(extractADFNodeText(li, indent+"  "))
		}
	case "orderedList":
		for i, li := range node.Content {
			line := fmt.Sprintf("%s%d. %s", indent, i+1, extractADFNodeText(li, indent+"  "))
			b.WriteString(line)
		}
	case "listItem":
		if len(node.Content) > 0 {
			for _, child := range node.Content {
				line := fmt.Sprintf("%s• %s", indent, strings.TrimSpace(extractADFNodeText(child, indent)))
				b.WriteString(line + "\n")
			}
		}
	case "panel":
		b.WriteString("\n--- Panel ---\n")
		for _, child := range node.Content {
			b.WriteString(extractADFNodeText(child, indent))
		}
		b.WriteString("\n--- End Panel ---\n")
	default:
		for _, child := range node.Content {
			b.WriteString(extractADFNodeText(child, indent))
		}
	}
	return b.String()
}

// MinimalJSON returns a JSON string with metadata fields stripped to save tokens.
func (r *SearchResponse) MinimalJSON() string {
	out := minimalResponse(*r)
	data, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func minimalResponse(r SearchResponse) SearchResponse {
	out := r
	minimalAccount := func(acc *TicketAccount) {
		if acc == nil {
			return
		}
		acc.Self = ""
		acc.AccountType = ""
		acc.AvatarUrls = AvatarUrls{}
		acc.AccountId = ""
		acc.EmailAddress = ""
	}
	minimalComment := func(c *TicketComment) {
		c.Self = ""
		c.Body = ADFNode{}
		minimalAccount(c.Author)
		minimalAccount(c.UpdateAuthor)
	}
	var minimalFields func(f *TicketFields)
	minimalFields = func(f *TicketFields) {
		if f == nil {
			return
		}
		f.Comment.Self = ""
		if f.Priority != nil {
			f.Priority.IconUrl = ""
			f.Priority.Self = ""
		}
		f.Description = ADFNode{}
		minimalAccount(f.Assignee)
		minimalAccount(f.Creator)
		for j := range f.Comment.Comments {
			minimalComment(&f.Comment.Comments[j])
		}
		for j := range f.Issuelinks {
			f.Issuelinks[j].Self = ""
			if f.Issuelinks[j].Type.Self != "" {
				f.Issuelinks[j].Type.Self = ""
			}
			f.Issuelinks[j].InwardIssue.Self = ""
			minimalFields(&f.Issuelinks[j].InwardIssue.Fields)
		}
		for j := range f.Components {
			f.Components[j].Self = ""
		}
	}
	for i := range out.Issues {
		is := &out.Issues[i]
		is.Self = ""
		is.Expand = ""
		minimalFields(&is.Fields)
	}
	return out
}
