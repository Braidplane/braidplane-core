package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestMFAFailedAttemptsPersistAndLock(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent=%t", concurrent), func(t *testing.T) {
			ctx := context.Background()
			pool := e2edb.OpenPostgresPool(t, "MFA failed attempts")
			repo := users.NewRepository()
			hasher := crypto.NewArgon2()
			codeHash, err := hasher.Hash("123456")
			require.NoError(t, err)
			subject := slug.Slug(fmt.Sprintf("mfa-attempts-%d", time.Now().UnixNano()))
			_, err = repo.CreateUser(ctx, pool, &users.CreateParams{Slug: subject, Email: subject.String() + "@example.com", FullName: "MFA Test", Password: codeHash, Role: model.RoleUser, Locale: "en"})
			require.NoError(t, err)
			t.Cleanup(func() { _, err := pool.Exec(ctx, "DELETE FROM users WHERE slug=$1", subject); require.NoError(t, err) })
			token := subject.String()
			digest := sha256.Sum256([]byte(token))
			tokenHash := hex.EncodeToString(digest[:])
			require.NoError(t, repo.CreateMFAChallenge(ctx, pool, subject, tokenHash, codeHash, time.Now().Add(10*time.Minute)))
			svc := userssvc.New(postgres.NewTransactionManager(pool), hasher, repo, nil, nil, "http://example.test")
			if concurrent {
				const attempts = 12
				results := make(chan error, attempts)
				start := make(chan struct{})
				var wg sync.WaitGroup
				for range attempts {
					wg.Go(func() {
						<-start
						user, err := svc.VerifySessionMFA(ctx, token, "000000")
						if user != nil {
							results <- fmt.Errorf("wrong code authenticated user")
							return
						}
						results <- err
					})
				}
				close(start)
				wg.Wait()
				close(results)
				for err := range results {
					require.ErrorIs(t, err, userssvc.ErrInvalidToken)
				}
			} else {
				for attempt := 1; attempt <= 5; attempt++ {
					user, err := svc.VerifySessionMFA(ctx, token, "000000")
					require.ErrorIs(t, err, userssvc.ErrInvalidToken)
					require.Nil(t, user)
					challenge, err := repo.FindMFAChallengeByToken(ctx, pool, tokenHash)
					require.NoError(t, err)
					require.Equal(t, attempt, challenge.AttemptCount)
				}
			}
			for _, code := range []string{"000000", "123456"} {
				user, err := svc.VerifySessionMFA(ctx, token, code)
				var rejection *xerror.Error
				require.ErrorAs(t, err, &rejection)
				require.Equal(t, userssvc.ErrMFAChallengeLocked, rejection)
				require.Nil(t, user)
			}
			challenge, err := repo.FindMFAChallengeByToken(ctx, pool, tokenHash)
			require.NoError(t, err)
			require.Equal(t, 5, challenge.AttemptCount)
			require.True(t, challenge.ConsumedAt.IsZero())
		})
	}
}
