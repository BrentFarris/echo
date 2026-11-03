package confluence

import (
	"strings"
	"testing"
)

func TestConfluenceStorageMarkdown(t *testing.T) {
	source := `<h2>Heading</h2><p>Hello <strong>bold</strong> and <em>emphasis</em> &amp; <a href="https://example.com/docs">a link</a>.</p><ul><li>First</li><li>Second<ul><li>Nested</li></ul></li></ul><table><tbody><tr><th>Name</th><th>Value</th></tr><tr><td>One</td><td>Two</td></tr></tbody></table><ac:structured-macro ac:name="code"><ac:parameter ac:name="language">go</ac:parameter><ac:plain-text-body><![CDATA[if a < b {
  fmt.Println("hello")
}]]></ac:plain-text-body></ac:structured-macro><ac:structured-macro ac:name="custom"><ac:rich-text-body><p>Preserved macro body</p></ac:rich-text-body></ac:structured-macro><script>unsafe()</script><a href="javascript:unsafe()">safe label</a>`
	result := Markdown(source)
	for _, want := range []string{"## Heading", "Hello **bold** and *emphasis* & [a link](https://example.com/docs).", "- First", "- Nested", "| Name | Value |", "| One | Two |", "```go\nif a < b {", "  fmt.Println", "[Confluence macro: custom]", "Preserved macro body", "safe label"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q in %s", want, result)
		}
	}
	for _, bad := range []string{"unsafe()", "javascript:"} {
		if strings.Contains(result, bad) {
			t.Errorf("unsafe output %s", result)
		}
	}
}

func TestConfluenceStorageReferencesPreserveLabelsAndTargets(t *testing.T) {
	source := `<p><ac:link><ri:page ri:space-key="ENG" ri:content-title="Team Guide"/><ac:plain-text-link-body><![CDATA[Read the guide]]></ac:plain-text-link-body></ac:link> <ac:link><ri:page ri:content-title="Local page"/><ac:plain-text-link-body>Local label</ac:plain-text-link-body></ac:link> <ac:link><ri:attachment ri:filename="design.pdf"/><ac:plain-text-link-body>Design</ac:plain-text-link-body></ac:link></p>`
	result := Markdown(source)
	for _, want := range []string{"[Read the guide](/display/ENG/Team%20Guide)", "Local label (Confluence page: Local page)", "Design (Confluence attachment: design.pdf)"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q in %s", want, result)
		}
	}
}
