package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestTreeRoot(t *testing.T) {
	var buf bytes.Buffer
	if err := Tree(&buf, &TreeNode{Label: "root"}); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if buf.String() != "root\n" {
		t.Errorf("Tree(root only) = %q, want %q", buf.String(), "root\n")
	}
}

func TestTreeNil(t *testing.T) {
	var buf bytes.Buffer
	if err := Tree(&buf, nil); err != nil {
		t.Fatalf("Tree(nil): %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("Tree(nil) wrote %q, want empty", buf.String())
	}
}

func TestTreeBranches(t *testing.T) {
	root := &TreeNode{
		Label: "root",
		Children: []*TreeNode{
			{Label: "a", Children: []*TreeNode{{Label: "a1"}}},
			{Label: "b"},
		},
	}
	var buf bytes.Buffer
	if err := Tree(&buf, root); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	want := []string{
		"root",
		"├── a",
		"│   └── a1",
		"└── b",
	}
	if len(lines) != len(want) {
		t.Fatalf("tree line count = %d, want %d; got:\n%s", len(lines), len(want), buf.String())
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}
