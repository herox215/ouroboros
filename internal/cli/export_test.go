package cli

// UserConfigDir is what finds where configurations are kept, for tests to get
// in the way.
var UserConfigDir = &userConfigDir

// Standard is what returns the plugins of a profile, for tests to get in the
// way.
var Standard = &standard

// Profile is what a profile hands over of itself, see Standard.
type Profile = profile
