package browser

import "context"

// Resolve credentials only when used, not while validating/saving settings.
func (b *browser) configuredRun(ctx context.Context, s settings, a args) (string, error) {
	s, err := s.normalized()
	if err != nil {
		return "", err
	}
	s.Token, err = browserToken(s.TokenEnv)
	if err != nil {
		return "", err
	}
	if s.Provider != "local" {
		return b.remotes.get(b.profile, s).run(ctx, s, a)
	}
	return b.run(ctx, s, a)
}
