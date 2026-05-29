package jess

import "errors"

// ErrRunInProgress is returned by Session.Prompt/Continue when a run is already
// active on that Session. Use Steer or FollowUp to inject into the running
// loop, or open another Session for a parallel conversation.
var ErrRunInProgress = errors.New("jess: a run is already in progress on this session")
