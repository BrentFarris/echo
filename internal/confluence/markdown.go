package confluence

import (
	"encoding/xml"
	"io"
	"net/url"
	"strings"
)

type markupNode struct {
	name     string
	attrs    map[string]string
	text     string
	children []*markupNode
}

// Markdown parses storage markup without executing HTML, macros, or fetching
// resources. Unknown macros remain visible together with their textual bodies.
func Markdown(source string) string {
	root := &markupNode{}
	stack := []*markupNode{root}
	decoder := xml.NewDecoder(strings.NewReader(source))
	decoder.Strict = false
	// HTML treats <link> as void; Confluence's <ac:link> contains a target
	// and label. encoding/xml matches AutoClose by local name, so exclude it.
	for _, name := range xml.HTMLAutoClose {
		if name != "link" {
			decoder.AutoClose = append(decoder.AutoClose, name)
		}
	}
	decoder.Entity = xml.HTMLEntity
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "[Storage markup conversion failed; request format=storage to read the original body.]\n\n" + source
		}
		switch v := token.(type) {
		case xml.StartElement:
			if len(stack) > 256 {
				return "[Storage markup is too deeply nested; request format=storage.]\n\n" + source
			}
			node := &markupNode{name: v.Name.Local, attrs: map[string]string{}}
			for _, a := range v.Attr {
				node.attrs[a.Name.Local] = a.Value
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, &markupNode{text: string(v)})
		}
	}
	return strings.TrimSpace(renderMarkup(root, 0, false))
}

func childrenText(n *markupNode, depth int, pre bool) string {
	var out strings.Builder
	for _, child := range n.children {
		out.WriteString(renderMarkup(child, depth, pre))
	}
	return out.String()
}
func safeLink(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.User != nil || strings.ContainsAny(link, "\r\n\x00") {
		return ""
	}
	if u.Scheme != "" && u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "mailto" {
		return ""
	}
	return strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29").Replace(link)
}
func plainText(n *markupNode) string {
	var out strings.Builder
	out.WriteString(n.text)
	for _, child := range n.children {
		out.WriteString(plainText(child))
	}
	return out.String()
}
func codeFence(value, language string) string {
	fence := "```"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	language = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '+' {
			return r
		}
		return -1
	}, language)
	return "\n\n" + fence + language + "\n" + value + "\n" + fence + "\n\n"
}
func renderMarkup(n *markupNode, depth int, pre bool) string {
	if n.name == "" && n.children == nil {
		if pre {
			return n.text
		}
		value := n.text
		if strings.TrimSpace(value) == "" {
			if strings.ContainsAny(value, "\r\n\t") {
				return ""
			}
			return " "
		}
		result := strings.Join(strings.Fields(value), " ")
		if strings.ContainsAny(value[:1], " \t\r\n") {
			result = " " + result
		}
		if strings.ContainsAny(value[len(value)-1:], " \t\r\n") {
			result += " "
		}
		return result
	}
	if n.name == "script" || n.name == "style" || n.name == "parameter" {
		return ""
	}
	if n.name == "pre" {
		return codeFence(plainText(n), "")
	}
	if n.name == "link" {
		var target *markupNode
		label := ""
		for _, child := range n.children {
			switch child.name {
			case "page", "attachment", "url":
				target = child
			case "plain-text-link-body", "link-body":
				label = strings.TrimSpace(childrenText(child, depth, pre))
			}
		}
		if target != nil {
			description := renderMarkup(target, depth, pre)
			if label == "" {
				label = description
			}
			link := ""
			switch target.name {
			case "url":
				link = safeLink(target.attrs["value"])
			case "page":
				if id := target.attrs["content-id"]; contentID.MatchString(id) {
					link = "/pages/viewpage.action?pageId=" + id
				} else if space := target.attrs["space-key"]; space != "" {
					link = "/display/" + url.PathEscape(space) + "/" + url.PathEscape(target.attrs["content-title"])
				}
			}
			if link != "" {
				return "[" + strings.ReplaceAll(label, "]", "\\]") + "](" + link + ")"
			}
			// Title-only references need Confluence's space context to resolve.
			// Preserve that reference rather than inventing a destination URL.
			return label + " (Confluence " + target.name + ": " + description + ")"
		}
	}
	if n.name == "structured-macro" || n.name == "macro" {
		name := n.attrs["name"]
		language := ""
		var body strings.Builder
		for _, child := range n.children {
			if child.name == "parameter" && child.attrs["name"] == "language" {
				language = plainText(child)
			}
			if child.name == "plain-text-body" {
				body.WriteString(plainText(child))
			} else if child.name != "parameter" {
				body.WriteString(renderMarkup(child, depth, pre))
			}
		}
		if name == "code" || name == "noformat" {
			return codeFence(body.String(), language)
		}
		return "\n\n[Confluence macro: " + name + "]\n\n" + body.String() + "\n\n"
	}
	if n.name == "ul" || n.name == "ol" {
		var out strings.Builder
		out.WriteString("\n")
		for _, child := range n.children {
			if child.name == "li" {
				marker := "- "
				if n.name == "ol" {
					marker = "1. "
				}
				text := strings.TrimSpace(childrenText(child, depth+1, pre))
				lines := strings.Split(text, "\n")
				out.WriteString(strings.Repeat("  ", depth) + marker + lines[0] + "\n")
				for _, line := range lines[1:] {
					if strings.TrimSpace(line) != "" {
						out.WriteString(strings.Repeat("  ", depth+1) + strings.TrimPrefix(line, strings.Repeat("  ", depth+1)) + "\n")
					}
				}
			}
		}
		return out.String() + "\n"
	}
	if n.name == "table" {
		var rows []*markupNode
		var visit func(*markupNode)
		visit = func(node *markupNode) {
			if node.name == "tr" {
				rows = append(rows, node)
				return
			}
			for _, child := range node.children {
				visit(child)
			}
		}
		visit(n)
		var out strings.Builder
		out.WriteString("\n\n")
		for i, row := range rows {
			cells := []string{}
			for _, cell := range row.children {
				if cell.name == "td" || cell.name == "th" {
					value := strings.TrimSpace(childrenText(cell, depth, pre))
					value = strings.NewReplacer("|", "\\|", "\n", "<br>").Replace(value)
					cells = append(cells, value)
				}
			}
			out.WriteString("| " + strings.Join(cells, " | ") + " |\n")
			if i == 0 {
				out.WriteString("|" + strings.Repeat(" --- |", len(cells)) + "\n")
			}
		}
		return out.String() + "\n"
	}
	value := childrenText(n, depth, pre)
	switch n.name {
	case "p", "div", "section", "rich-text-body", "blockquote":
		return "\n\n" + value + "\n\n"
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return "\n\n" + strings.Repeat("#", int(n.name[1]-'0')) + " " + strings.TrimSpace(value) + "\n\n"
	case "br":
		return "\n"
	case "hr":
		return "\n\n---\n\n"
	case "strong", "b":
		return "**" + value + "**"
	case "em", "i":
		return "*" + value + "*"
	case "code":
		fence := "`"
		for strings.Contains(value, fence) {
			fence += "`"
		}
		return fence + " " + value + " " + fence
	case "a":
		if link := safeLink(n.attrs["href"]); link != "" {
			return "[" + strings.ReplaceAll(value, "]", "\\]") + "](" + link + ")"
		}
		return value
	case "img":
		return "[Image: " + n.attrs["alt"] + "]"
	case "image":
		if value == "" {
			return "[Confluence image]"
		}
		return "[Image: " + value + "]"
	case "page":
		return n.attrs["content-title"]
	case "attachment":
		return n.attrs["filename"]
	case "url":
		if link := safeLink(n.attrs["value"]); link != "" {
			return link
		}
		return ""
	default:
		return value
	}
}
