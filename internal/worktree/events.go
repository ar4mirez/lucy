package worktree

import "github.com/ar4mirez/lucy/internal/eventstore"

// A repository's slots, on its stream "worktree/<org>/<repo>". The pool's state is the
// worktree_slots projection of them, so a restart knows every slot and who holds it.

// StreamPrefix starts every pool stream.
const StreamPrefix = "worktree/"

// Stream is org's repo's pool stream.
func Stream(org, repo string) string { return StreamPrefix + org + "/" + repo }

// Added records a new slot: a worktree of the repository at Path.
type Added struct {
	Org  string `json:"org"`
	Repo string `json:"repo"`
	Slot int    `json:"slot"`
	Path string `json:"path"`
}

// Leased records a run taking a slot: it is reset to Base at BaseSHA and the run works on Branch.
type Leased struct {
	Slot    int    `json:"slot"`
	Run     string `json:"run"`
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	BaseSHA string `json:"base_sha"`
}

// Released records a run giving a slot back with its work landed: the proof names how (Proof*),
// and Head is the commit the slot held.
type Released struct {
	Slot  int    `json:"slot"`
	Run   string `json:"run"`
	Head  string `json:"head"`
	Proof string `json:"proof"`
	Merge string `json:"merge,omitempty"` // with ProofMerged: the merge commit on the base branch
}

// Broken records a slot that could not be reset or made: it is never leased again.
type Broken struct {
	Slot   int    `json:"slot"`
	Reason string `json:"reason"`
}

// How a release's work was proven landed.
const (
	ProofEmpty    = "empty"    // the slot holds no commit beyond the base it was leased at
	ProofAncestor = "ancestor" // the slot's head is on the base branch
	ProofMerged   = "merged"   // the forge merged exactly the slot's head, and the merge is on the base branch
)

func (Added) EventType() string    { return "worktree.added" }
func (Leased) EventType() string   { return "worktree.leased" }
func (Released) EventType() string { return "worktree.released" }
func (Broken) EventType() string   { return "worktree.broken" }

func (Added) EventVersion() int    { return 1 }
func (Leased) EventVersion() int   { return 1 }
func (Released) EventVersion() int { return 1 }
func (Broken) EventVersion() int   { return 1 }

// Payloads returns a sample of every pool event, for an eventstore.Registry.
func Payloads() []eventstore.Payload {
	return []eventstore.Payload{Added{}, Leased{}, Released{}, Broken{}}
}
