package output

import (
	"fmt"
	"io"
)

// TreeNode is one node of the ASCII tree rendered by Tree. Label is
// the line shown for this node; Children are drawn below it in the
// supplied order (no sorting — callers decide order, e.g. quest graph
// emits children sorted by status then id). Task 10.3's graph command
// wires this up for real; Phase 4 ships the skeleton so the output
// package surface is complete.
type TreeNode struct {
	Label    string
	Children []*TreeNode
}

// Tree writes root to w using box-drawing branch connectors. The root
// label is emitted without a connector; descendants use ├── / └──
// with │ / space continuation prefixes. Nil root writes nothing.
func Tree(w io.Writer, root *TreeNode) error {
	if root == nil {
		return nil
	}
	if _, err := fmt.Fprintln(w, root.Label); err != nil {
		return err
	}
	return writeChildren(w, root.Children, "")
}

func writeChildren(w io.Writer, children []*TreeNode, prefix string) error {
	for i, c := range children {
		isLast := i == len(children)-1
		connector := "├── "
		nextPrefix := prefix + "│   "
		if isLast {
			connector = "└── "
			nextPrefix = prefix + "    "
		}
		if _, err := fmt.Fprintln(w, prefix+connector+c.Label); err != nil {
			return err
		}
		if err := writeChildren(w, c.Children, nextPrefix); err != nil {
			return err
		}
	}
	return nil
}
