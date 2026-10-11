package engine

import (
	"path/filepath"
	"sort"
	"strings"
)

// itemRef addresses findings[finding].Items[item]. Positions, not item
// ids: two items may share an id (FillItemKeys does not uniquify).
type itemRef struct{ finding, item int }

// forest is the containment forest over every item of a scan: an
// item's parent is the nearest item, from any rule, whose path
// strictly contains it; on equal paths the later rule in catalog order
// (findings order) is the child. Pathless items are roots.
type forest struct {
	parent   map[itemRef]itemRef
	children map[itemRef][]itemRef
	paths    map[itemRef]string
}

func buildForest(findings []Finding) forest {
	f := forest{
		parent:   map[itemRef]itemRef{},
		children: map[itemRef][]itemRef{},
		paths:    map[itemRef]string{},
	}
	type node struct {
		ref itemRef
		key string // cleaned path + "/": a dir's descendants share the prefix
	}
	var nodes []node
	for fi, fd := range findings {
		for ii, it := range fd.Items {
			if it.Path == "" {
				continue
			}
			ref := itemRef{fi, ii}
			p := filepath.Clean(it.Path)
			f.paths[ref] = p
			key := p + "/"
			if p == "/" {
				key = "/"
			}
			nodes = append(nodes, node{ref, key})
		}
	}
	// The trailing slash keeps each subtree contiguous in sort order:
	// bare paths would sort "/a/b-x" between "/a/b" and "/a/b/c" and
	// break the stack walk below.
	sort.Slice(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.key != b.key {
			return a.key < b.key
		}
		if a.ref.finding != b.ref.finding {
			return a.ref.finding < b.ref.finding
		}
		return a.ref.item < b.ref.item
	})
	var stack []node
	for _, n := range nodes {
		for len(stack) > 0 && !strings.HasPrefix(n.key, stack[len(stack)-1].key) {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			p := stack[len(stack)-1].ref
			f.parent[n.ref] = p
			f.children[p] = append(f.children[p], n.ref)
		}
		stack = append(stack, n)
	}
	return f
}

// ancestors returns ref's ancestors, nearest first.
func (f forest) ancestors(ref itemRef) []itemRef {
	var out []itemRef
	for p, ok := f.parent[ref]; ok; p, ok = f.parent[p] {
		out = append(out, p)
	}
	return out
}

// descendants returns every item below ref, depth first.
func (f forest) descendants(ref itemRef) []itemRef {
	var out []itemRef
	stack := append([]itemRef(nil), f.children[ref]...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, n)
		stack = append(stack, f.children[n]...)
	}
	return out
}

// sharesPath lists ref's ancestors at exactly ref's path: the same
// target claimed by an earlier rule.
func (f forest) sharesPath(ref itemRef) []itemRef {
	var out []itemRef
	for _, a := range f.ancestors(ref) {
		if f.paths[a] != f.paths[ref] {
			break
		}
		out = append(out, a)
	}
	return out
}
