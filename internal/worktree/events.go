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

// Leased records a run taking a slot, which is then being prepared: the run works on Branch, on
// Base. BaseSHA is empty on a fresh lease, whose base commit Prepared records; on a lease of a
// slot the run holds already it is the commit the run's lease was made at.
type Leased struct {
	Slot    int    `json:"slot"`
	Run     string `json:"run"`
	Branch  string `json:"branch"`
	Base    string `json:"base"`
	BaseSHA string `json:"base_sha"`
}

// Prepared records a leased slot made ready for its run: its worktree is at BaseSHA, on the run's
// branch. Only then does the run's work start in it.
type Prepared struct {
	Slot    int    `json:"slot"`
	Run     string `json:"run"`
	BaseSHA string `json:"base_sha"`
}

// PrepareFailed records a leased slot that could not be made ready for its run, for a reason that
// is not the slot's own (the base, the remote or the run's branch), or because the preparation
// stopped (PrepareStopped). The run sees its worktree failed. A fresh lease, which never held the
// run's work, is given back free; a slot the run held already stays leased to it.
type PrepareFailed struct {
	Slot   int    `json:"slot"`
	Run    string `json:"run"`
	Reason string `json:"reason"`
}

// PrepareStopped is a PrepareFailed's reason when the preparation was cancelled or ran out of time.
const PrepareStopped = "the preparation stopped before it finished: the daemon stopped, or it ran out of time"

// Released records a run giving a slot back with its work landed: the proof names how (Proof*),
// and Head is the commit the slot held.
type Released struct {
	Slot  int    `json:"slot"`
	Run   string `json:"run"`
	Head  string `json:"head"`
	Proof string `json:"proof"`
	Merge string `json:"merge,omitempty"` // with ProofMerged: the merge commit on the base branch
}

// Quarantined records a slot whose run finished with work that has not landed: it is kept, its
// work untouched, never leased again and not counted against the pool's size, until a later
// release finds the work landed or the owner discards it.
type Quarantined struct {
	Slot   int    `json:"slot"`
	Run    string `json:"run"`
	Head   string `json:"head"`
	Reason string `json:"reason"` // Quarantined*
}

// Why a slot is quarantined.
const (
	QuarantinedUncommitted = "uncommitted changes"
	QuarantinedUnpushed    = "commits on no remote ref"
	QuarantinedUnchecked   = "its work could not be checked" // the remote could not be fetched, or the worktree read
)

// Broken records a slot that could not be reset or made: it is never leased again.
type Broken struct {
	Slot   int    `json:"slot"`
	Reason string `json:"reason"`
}

// Discarded records the owner discarding a quarantined or broken slot, before its directory is
// removed. Its branch stays in base.git, so its commits are recoverable until git gc. The slot's
// number is never used again.
type Discarded struct {
	Slot   int    `json:"slot"`
	Run    string `json:"run"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
	By     string `json:"by"`
}

// How a release's work was proven landed.
const (
	ProofEmpty      = "empty"      // the slot holds no commit beyond the base it was leased at
	ProofAncestor   = "ancestor"   // the slot's head is on the base branch
	ProofMerged     = "merged"     // the forge merged exactly the slot's head, and the merge is on the base branch
	ProofPushed     = "pushed"     // every commit of the slot's head is on a ref of the remote
	ProofUnprepared = "unprepared" // the slot was never made ready for the run, so its work never started in it
)

func (Added) EventType() string         { return "worktree.added" }
func (Leased) EventType() string        { return "worktree.leased" }
func (Prepared) EventType() string      { return "worktree.prepared" }
func (PrepareFailed) EventType() string { return "worktree.prepare_failed" }
func (Released) EventType() string      { return "worktree.released" }
func (Quarantined) EventType() string   { return "worktree.quarantined" }
func (Broken) EventType() string        { return "worktree.broken" }
func (Discarded) EventType() string     { return "worktree.discarded" }

func (Added) EventVersion() int         { return 1 }
func (Leased) EventVersion() int        { return 1 }
func (Prepared) EventVersion() int      { return 1 }
func (PrepareFailed) EventVersion() int { return 1 }
func (Released) EventVersion() int      { return 1 }
func (Quarantined) EventVersion() int   { return 1 }
func (Broken) EventVersion() int        { return 1 }
func (Discarded) EventVersion() int     { return 1 }

// Payloads returns a sample of every pool event, for an eventstore.Registry.
func Payloads() []eventstore.Payload {
	return []eventstore.Payload{Added{}, Leased{}, Prepared{}, PrepareFailed{}, Released{}, Quarantined{}, Broken{}, Discarded{}}
}
