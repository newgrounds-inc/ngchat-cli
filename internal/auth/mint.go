package auth

import "context"

// Minter obtains a fresh chat JWT. The token lives 1 hour and the chat
// server hard-closes the socket when it lapses, so minting recurs for the
// life of a session.
type Minter interface {
	Mint(ctx context.Context) (string, error)
}

// ServiceTokenMinter re-mints through POST /api/v1/auth/service-token
// with the run's cookie jar (ADR 0003). Callers mint exactly once per
// connect and once per server revalidate: the site's service_token
// limiter (10/min per user) counts before auth and CSRF, so a retry loop
// would lock the user's browser out of chat too. A 401 surfaces as
// ErrSignedOut and must stop the client rather than reconnect.
type ServiceTokenMinter struct {
	Site *Site
}

// Mint implements Minter.
func (m *ServiceTokenMinter) Mint(ctx context.Context) (string, error) {
	return m.Site.ServiceToken(ctx, ChatService)
}
