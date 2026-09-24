package main

import (
	"testing"

	"bunker/internal/vt10x"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makePane returns a Pane with only the fields layout code needs.
func makePane(id, x, y, w, h int) *Pane {
	return &Pane{id: id, x: x, y: y, w: w, h: h}
}

// makePaneWithTerm returns a Pane that has a valid vt10x.Terminal so
// pane.resize (called by Node.split) does not panic.
func makePaneWithTerm(id, x, y, w, h int) *Pane {
	cols := w - 1 // last column reserved for scrollbar
	if cols < 1 {
		cols = 1
	}
	t := vt10x.New(vt10x.WithSize(cols, h))
	return &Pane{id: id, x: x, y: y, w: w, h: h, term: t}
}

// ---------------------------------------------------------------------------
// Node.leaves
// ---------------------------------------------------------------------------

func TestLeaves_SingleLeaf(t *testing.T) {
	p := makePane(1, 0, 0, 80, 24)
	n := &Node{pane: p, x: 0, y: 0, w: 80, h: 24}

	got := n.leaves()
	if len(got) != 1 {
		t.Fatalf("leaves() returned %d nodes, want 1", len(got))
	}
	if got[0] != n {
		t.Error("leaves()[0] is not the original node")
	}
}

func TestLeaves_AfterSplit(t *testing.T) {
	p1 := makePaneWithTerm(1, 0, 0, 80, 24)
	p2 := makePaneWithTerm(2, 0, 0, 80, 24)
	root := &Node{pane: p1, x: 0, y: 0, w: 80, h: 24}

	root.split(p2, splitVertical)

	got := root.leaves()
	if len(got) != 2 {
		t.Fatalf("leaves() returned %d nodes, want 2", len(got))
	}
	if got[0].pane != p1 {
		t.Error("leaves()[0] should hold the original pane")
	}
	if got[1].pane != p2 {
		t.Error("leaves()[1] should hold the new pane")
	}
}

func TestLeaves_NestedSplits(t *testing.T) {
	p1 := makePaneWithTerm(1, 0, 0, 80, 24)
	p2 := makePaneWithTerm(2, 0, 0, 80, 24)
	p3 := makePaneWithTerm(3, 0, 0, 80, 24)
	root := &Node{pane: p1, x: 0, y: 0, w: 80, h: 24}

	root.split(p2, splitVertical)
	// Split the left child again.
	root.left.split(p3, splitHorizontal)

	got := root.leaves()
	if len(got) != 3 {
		t.Fatalf("leaves() returned %d nodes, want 3", len(got))
	}
	// Depth-first order: left-left, left-right, right.
	if got[0].pane != p1 {
		t.Error("leaves()[0] should be p1 (left-left)")
	}
	if got[1].pane != p3 {
		t.Error("leaves()[1] should be p3 (left-right)")
	}
	if got[2].pane != p2 {
		t.Error("leaves()[2] should be p2 (right)")
	}
}

// ---------------------------------------------------------------------------
// Node.split (vertical)
//
// Bug: btop corruption when starting small then maximizing
//
// Split recalculates pane dimensions and triggers resize. If the geometry
// is wrong, alt-screen apps like btop get a SIGWINCH for the wrong size
// and render garbage.
// ---------------------------------------------------------------------------

func TestSplit_Vertical_BtopResizeGeometry(t *testing.T) {
	p1 := makePaneWithTerm(1, 0, 0, 80, 24)
	p2 := makePaneWithTerm(2, 0, 0, 80, 24)
	root := &Node{pane: p1, x: 0, y: 0, w: 80, h: 24}

	root.split(p2, splitVertical)

	if root.isLeaf() {
		t.Fatal("root should be an internal node after split")
	}

	left := root.left
	right := root.right

	// half = 80 / 2 = 40
	// left:  x=0, y=0, w=40, h=24
	// right: x=41, y=0, w=39, h=24  (border at column 40)
	if left.w != 40 {
		t.Errorf("left.w = %d, want 40", left.w)
	}
	if left.h != 24 {
		t.Errorf("left.h = %d, want 24", left.h)
	}
	if right.w != 39 {
		t.Errorf("right.w = %d, want 39", right.w)
	}
	if right.h != 24 {
		t.Errorf("right.h = %d, want 24", right.h)
	}
	if right.x != left.x+41 {
		t.Errorf("right.x = %d, want %d (left.x + half + 1 border)", right.x, left.x+41)
	}
	if right.y != 0 {
		t.Errorf("right.y = %d, want 0", right.y)
	}
}

// ---------------------------------------------------------------------------
// Node.split (horizontal)
//
// Bug: btop corruption when starting small then maximizing
// ---------------------------------------------------------------------------

func TestSplit_Horizontal_BtopResizeGeometry(t *testing.T) {
	p1 := makePaneWithTerm(1, 0, 0, 80, 24)
	p2 := makePaneWithTerm(2, 0, 0, 80, 24)
	root := &Node{pane: p1, x: 0, y: 0, w: 80, h: 24}

	root.split(p2, splitHorizontal)

	if root.isLeaf() {
		t.Fatal("root should be an internal node after split")
	}

	left := root.left
	right := root.right

	// half = 24 / 2 = 12
	// top:    x=0, y=0,  w=80, h=12
	// bottom: x=0, y=13, w=80, h=11  (border at row 12)
	if left.h != 12 {
		t.Errorf("top.h = %d, want 12", left.h)
	}
	if left.w != 80 {
		t.Errorf("top.w = %d, want 80", left.w)
	}
	if right.h != 11 {
		t.Errorf("bottom.h = %d, want 11", right.h)
	}
	if right.w != 80 {
		t.Errorf("bottom.w = %d, want 80", right.w)
	}
	if right.y != left.y+13 {
		t.Errorf("bottom.y = %d, want %d (top.y + half + 1 border)", right.y, left.y+13)
	}
	if right.x != 0 {
		t.Errorf("bottom.x = %d, want 0", right.x)
	}
}

// ---------------------------------------------------------------------------
// removeFromTree
// ---------------------------------------------------------------------------

func TestRemoveFromTree_SingleLeaf(t *testing.T) {
	p := makePane(1, 0, 0, 80, 24)
	root := &Node{pane: p, x: 0, y: 0, w: 80, h: 24}

	got := removeFromTree(root, p)
	if got != nil {
		t.Errorf("removeFromTree(single leaf) = %v, want nil", got)
	}
}

func TestRemoveFromTree_RemoveLeft(t *testing.T) {
	p1 := makePaneWithTerm(1, 0, 0, 80, 24)
	p2 := makePaneWithTerm(2, 0, 0, 80, 24)
	root := &Node{pane: p1, x: 0, y: 0, w: 80, h: 24}
	root.split(p2, splitVertical)

	newRoot := removeFromTree(root, p1)
	if newRoot == nil {
		t.Fatal("removeFromTree returned nil, want surviving sibling")
	}
	if newRoot.pane != p2 {
		t.Error("surviving pane should be p2")
	}
	// Sibling should be promoted to parent's full bounds.
	if newRoot.x != 0 || newRoot.y != 0 || newRoot.w != 80 || newRoot.h != 24 {
		t.Errorf("promoted sibling bounds = (%d,%d,%d,%d), want (0,0,80,24)",
			newRoot.x, newRoot.y, newRoot.w, newRoot.h)
	}
}

func TestRemoveFromTree_RemoveRight(t *testing.T) {
	p1 := makePaneWithTerm(1, 0, 0, 80, 24)
	p2 := makePaneWithTerm(2, 0, 0, 80, 24)
	root := &Node{pane: p1, x: 0, y: 0, w: 80, h: 24}
	root.split(p2, splitVertical)

	newRoot := removeFromTree(root, p2)
	if newRoot == nil {
		t.Fatal("removeFromTree returned nil, want surviving sibling")
	}
	if newRoot.pane != p1 {
		t.Error("surviving pane should be p1")
	}
	if newRoot.x != 0 || newRoot.y != 0 || newRoot.w != 80 || newRoot.h != 24 {
		t.Errorf("promoted sibling bounds = (%d,%d,%d,%d), want (0,0,80,24)",
			newRoot.x, newRoot.y, newRoot.w, newRoot.h)
	}
}

func TestRemoveFromTree_NonExistentPane(t *testing.T) {
	p1 := makePaneWithTerm(1, 0, 0, 80, 24)
	p2 := makePaneWithTerm(2, 0, 0, 80, 24)
	ghost := makePane(99, 0, 0, 80, 24) // not in tree
	root := &Node{pane: p1, x: 0, y: 0, w: 80, h: 24}
	root.split(p2, splitVertical)

	origLeft := root.left
	origRight := root.right
	newRoot := removeFromTree(root, ghost)
	if newRoot != root {
		t.Error("removeFromTree should return original root when pane not found")
	}
	if root.left != origLeft || root.right != origRight {
		t.Error("tree structure should be unchanged")
	}
}
