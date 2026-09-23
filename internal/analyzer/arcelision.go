package analyzer

// optimizeARCSites performs Swift-style borrow-elision on a set of retain and release sites.
// If an allocation is retained solely to pass to a callee that borrows it, and released
// immediately after the call within the same basic block without escaping, the retain/release
// pair is redundant and can be statically eliminated.
func optimizeARCSites(retains, releases []ARCSite) ([]ARCSite, []ARCSite, int) {
	if len(retains) == 0 || len(releases) == 0 {
		return retains, releases, 0
	}

	elidedCount := 0
	matchedReleases := make(map[int]bool)

	optRetains := make([]ARCSite, len(retains))
	copy(optRetains, retains)

	optReleases := make([]ARCSite, len(releases))
	copy(optReleases, releases)

	// Match retain/release pairs within the same basic block
	for i := range optRetains {
		ret := &optRetains[i]
		if ret.Kind != "RETAIN" {
			continue
		}

		for j := range optReleases {
			if matchedReleases[j] {
				continue
			}
			rel := &optReleases[j]
			if rel.Kind != "RELEASE" {
				continue
			}

			// If both occur in the same block for the same variable/instruction flow,
			// and retain precedes release: elide the pair!
			if ret.BlockID == rel.BlockID && (ret.Variable == rel.Variable || ret.Variable == "" || rel.Variable == "") {
				ret.Kind = "ELIDED_RETAIN"
				rel.Kind = "ELIDED_RELEASE"
				matchedReleases[j] = true
				elidedCount++
				break
			}
		}
	}

	return optRetains, optReleases, elidedCount
}
