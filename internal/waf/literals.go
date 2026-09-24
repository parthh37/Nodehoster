package waf

// The literal prefilter. Every rule names substrings its pattern cannot
// match without (the OWASP CRS's "pm" operator does the same job): a value
// is scanned once for all of them with an Aho-Corasick automaton, and a
// rule's regular expression only runs when its literals are there. Most
// values of most requests contain none, so most rules cost nothing.

const (
	maxLiterals = 512
	litWords    = maxLiterals / 64
)

// litSet is a set of literal indexes.
type litSet [litWords]uint64

func (s *litSet) add(i int) { s[i>>6] |= 1 << (i & 63) }
func (s *litSet) or(o *litSet) {
	for i := range s {
		s[i] |= o[i]
	}
}
func (s *litSet) intersects(o *litSet) bool {
	for i := range s {
		if s[i]&o[i] != 0 {
			return true
		}
	}
	return false
}
func (s *litSet) empty() bool {
	for _, w := range s {
		if w != 0 {
			return false
		}
	}
	return true
}

// litIndex assigns literals their indexes while the rules are built.
type litIndex struct {
	lits []string
	idx  map[string]int
}

func (x *litIndex) id(lit string) int {
	if i, ok := x.idx[lit]; ok {
		return i
	}
	if len(x.lits) == maxLiterals {
		panic("waf: too many rule literals")
	}
	if x.idx == nil {
		x.idx = map[string]int{}
	}
	x.idx[lit] = len(x.lits)
	x.lits = append(x.lits, lit)
	return len(x.lits) - 1
}

// automaton is a deterministic Aho-Corasick automaton over byte classes:
// bytes that occur in no literal share class 0 (which always leads back
// to the root), so the table has a column per distinct literal byte
// rather than 256.
type automaton struct {
	class  [256]uint8
	width  int     // classes
	next   []int32 // state*width + class -> state
	outIdx []int32 // state -> index into outs; 0 = no literal ends here
	outs   []litSet
}

func buildAutomaton(lits []string) *automaton {
	a := &automaton{width: 1}
	for _, l := range lits {
		for i := 0; i < len(l); i++ {
			if a.class[l[i]] == 0 {
				if a.width == 256 {
					panic("waf: literal alphabet too large")
				}
				a.class[l[i]] = uint8(a.width)
				a.width++
			}
		}
	}
	// The trie, with -1 for missing edges.
	type node struct {
		out  litSet
		has  bool
		fail int32
	}
	nodes := []node{{}}
	goTo := [][]int32{make([]int32, a.width)}
	for i := range goTo[0] {
		goTo[0][i] = -1
	}
	for li, l := range lits {
		s := int32(0)
		for i := 0; i < len(l); i++ {
			c := a.class[l[i]]
			if goTo[s][c] < 0 {
				row := make([]int32, a.width)
				for j := range row {
					row[j] = -1
				}
				goTo = append(goTo, row)
				nodes = append(nodes, node{})
				goTo[s][c] = int32(len(nodes) - 1)
			}
			s = goTo[s][c]
		}
		nodes[s].out.add(li)
		nodes[s].has = true
	}
	// Breadth-first: failure links, outputs inherited along them, and
	// missing edges filled in so that scanning never follows a link.
	queue := make([]int32, 0, len(nodes))
	for c := 0; c < a.width; c++ {
		if t := goTo[0][c]; t >= 0 {
			nodes[t].fail = 0
			queue = append(queue, t)
		} else {
			goTo[0][c] = 0
		}
	}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		f := nodes[s].fail
		if nodes[f].has {
			nodes[s].out.or(&nodes[f].out)
			nodes[s].has = true
		}
		for c := 0; c < a.width; c++ {
			t := goTo[s][c]
			if t < 0 {
				goTo[s][c] = goTo[f][c]
				continue
			}
			nodes[t].fail = goTo[f][c]
			queue = append(queue, t)
		}
	}
	a.next = make([]int32, len(nodes)*a.width)
	a.outIdx = make([]int32, len(nodes))
	a.outs = []litSet{{}} // index 0: nothing
	for s := range nodes {
		copy(a.next[s*a.width:], goTo[s])
		if nodes[s].has {
			a.outIdx[s] = int32(len(a.outs))
			a.outs = append(a.outs, nodes[s].out)
		}
	}
	return a
}

// scan adds to found the literals that occur in s.
func (a *automaton) scan(s string, found *litSet) {
	st := int32(0)
	w := int32(a.width)
	for i := 0; i < len(s); i++ {
		st = a.next[st*w+int32(a.class[s[i]])]
		if o := a.outIdx[st]; o != 0 {
			found.or(&a.outs[o])
		}
	}
}
