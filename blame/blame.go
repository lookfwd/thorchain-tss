package blame

import (
	"bytes"
	"fmt"
	"strings"
)

func NewBlameNode(pk string, blameData, blameSig []byte) Node {
	return Node{
		Pubkey:         pk,
		BlameData:      blameData,
		BlameSignature: blameSig,
	}
}

func (bn *Node) Equal(node Node) bool {
	if bn.Pubkey == node.Pubkey && bytes.Equal(bn.BlameSignature, node.BlameSignature) {
		return true
	}
	return false
}

// NewBlame create a new instance of Blame
func NewBlame(reason string, blameNodes []Node) Blame {
	return Blame{
		FailReason: reason,
		BlameNodes: blameNodes,
	}
}

// IsEmpty check whether it is empty
func (b *Blame) IsEmpty() bool {
	return len(b.FailReason) == 0
}

// String implement fmt.Stringer
func (b Blame) String() string {
	sb := strings.Builder{}
	sb.WriteString("reason:" + b.FailReason + "\n")
	sb.WriteString(fmt.Sprintf("nodes:%+v\n", b.BlameNodes))
	return sb.String()
}

// SetBlame update the field values of Blame
func (b *Blame) SetBlame(reason string, nodes []Node) {
	b.FailReason = reason
	b.BlameNodes = append(b.BlameNodes, nodes...)
}

func (b *Blame) AlreadyBlame() bool {
	if len(b.BlameNodes) != 0 {
		return true
	}
	return false
}

// AddBlameNodes add nodes to the blame list
func (b *Blame) AddBlameNodes(newBlameNodes ...Node) {
	for _, node := range newBlameNodes {
		found := false
		for _, el := range b.BlameNodes {
			if node.Equal(el) {
				found = true
				break
			}
		}
		if !found {
			b.BlameNodes = append(b.BlameNodes, node)
		}
	}
}