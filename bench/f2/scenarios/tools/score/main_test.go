package main

import "testing"

// The first batch's defect: a recording that only echoes the S1 prompt
// (never submitted) must not count as an answer.
func TestPromptEchoIsNotAnAnswer(t *testing.T) {
	echo := "› Explain what simplelru/lru.go does and how eviction works.\n  gemma4:latest default · /tmp/fx\n"
	if reS1.MatchString(output(echo, "S1")) {
		t.Fatal("the prompt's own words counted as an answer")
	}
	ans := echo + "It keeps a doubly linked list; Get calls MoveToFront and eviction removes the least recently used entry.\n"
	if !reS1.MatchString(output(ans, "S1")) {
		t.Fatal("a real answer wasn't recognised")
	}
}
