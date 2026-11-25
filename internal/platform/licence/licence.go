// Package licence holds how a licence with conditions is described to a client before the person installs something that comes under it: its name,
// where to read it, whether it allows commercial use and a one-line summary. The server only describes; it never accepts a licence for anyone
// (see Acceptance for the licences that need the person's explicit yes).
package licence

// Commercial says whether a licence allows commercial use.
const (
	Yes        = "yes"
	Conditions = "conditions"
	No         = "no"
	Unknown    = "unknown"
)

// Terms is a licence as a client shows it, above the button that confirms an install.
type Terms struct {
	// ID is set where an acceptance is required: the value a client sends back as accept_licence once the person agrees.
	ID string `json:"id,omitempty"`
	// Name is the licence's name, URL where its text is and Summary its conditions in one line.
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Commercial is yes, conditions, no or unknown.
	Commercial string `json:"commercial"`
	// Accepted says an acceptance was recorded on this server; omitted where none is required.
	Accepted *bool `json:"accepted,omitempty"`
}

// StabilityCommunity is the id of the Stability AI Community License, the one acceptance this program asks for explicitly: Stable Fast 3D, its port and
// its weights are under it.
const StabilityCommunity = "stability-community"

// StabilityCommunityTerms are the terms of Stable Fast 3D.
var StabilityCommunityTerms = Terms{
	ID:         StabilityCommunity,
	Name:       "Stability AI Community License",
	URL:        "https://stability.ai/community-license-agreement",
	Commercial: Conditions,
	Summary: "Free for research and non-commercial use and for commercial use by people and organisations under US$1M of yearly revenue, " +
		"who register with Stability AI; above that, a licence from Stability AI. Attribution is asked, and the weights need a Hugging Face account that accepted it.",
}
