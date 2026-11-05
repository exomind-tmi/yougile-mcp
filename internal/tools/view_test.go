package tools

import "testing"

func TestAPlainChatMessageKeepsItsBracketsAndAmpersands(t *testing.T) {
	got := messageText("Fix <b>it</b> & go\n\n  next\n/root/#file:/user-data/x.png", "")
	if got != "Fix <b>it</b> & go\nnext" {
		t.Errorf("got %q", got)
	}
	if got := messageText("ignored", "<p>A &amp; B</p><p>C</p>"); got != "A & B\nC" {
		t.Errorf("html: %q", got)
	}
}

func TestParagraphsMakeTheHTMLOfADescription(t *testing.T) {
	for in, want := range map[string]string{
		"One\nline two\n\nPara <2> & 3": "<p>One<br>line two</p><p>Para &lt;2&gt; &amp; 3</p>",
		"\n\nonly\n\n\n":                "<p>only</p>",
		"":                              "",
		"a\r\n\r\nb":                    "<p>a</p><p>b</p>",
	} {
		if got := paragraphs(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestFileTokensAreWrittenAsTheYouGileAppWritesThem(t *testing.T) {
	if got := fileTokenOf("/user-data/abc/Build%20log.txt"); got != "/root/#file:/user-data/abc/Build%2520log.txt" {
		t.Errorf("got %s", got)
	}
}
