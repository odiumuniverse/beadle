package state

import "slices"

// PublishConsent is the authority one caller has to install a package without
// asking the user a question.
//
// It exists because "the user already agreed" is a fact some callers can prove
// and others cannot. A state an older build wrote can record that a host had a
// bundle enabled — the user switched it on, which is their consent — and the
// upgrade may act on exactly that, for exactly the packages named here, on
// exactly the hosts named here. Everything else is declined, so a question
// about a package nobody approved is still a question.
//
// The Reason travels into the report: a consent nobody can read is a consent
// nobody can audit.
type PublishConsent struct {
	// ApprovedFor are the package ids the caller consents to. A question about
	// any other package is declined.
	ApprovedFor []string
	// Hosts are the hosts the consent covers. A question about any other host
	// is declined.
	Hosts []string
	// Reason is why the consent exists, in the words a user would recognise.
	Reason string
}

// Approves reports whether this consent covers one question. Both halves must
// match: a consent for the canon package is not a consent for a third-party
// plugin that happens to be installed in the same run.
func (c PublishConsent) Approves(packageID, host string) bool {
	if c.Reason == "" {
		return false
	}

	return slices.Contains(c.ApprovedFor, packageID) && slices.Contains(c.Hosts, host)
}
