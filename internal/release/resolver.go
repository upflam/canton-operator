package release

import "context"

// Resolution is a group's target version; Skip means do nothing this cycle.
type Resolution struct {
	Version string
	Skip    bool
	Reason  string
}

// VersionResolver yields the target version for one update group.
type VersionResolver interface {
	Resolve(ctx context.Context) (Resolution, error)
}

// ConsensusResolver adapts Checker to VersionResolver.
type ConsensusResolver struct{ c *Checker }

func NewConsensusResolver(c *Checker) *ConsensusResolver { return &ConsensusResolver{c: c} }

func (r *ConsensusResolver) Resolve(ctx context.Context) (Resolution, error) {
	d, err := r.c.Desired(ctx)
	if err != nil {
		return Resolution{}, err
	}
	if !d.HaveConsensus {
		return Resolution{Version: d.Version, Skip: true, Reason: d.Reason}, nil
	}
	return Resolution{Version: d.Version}, nil
}
