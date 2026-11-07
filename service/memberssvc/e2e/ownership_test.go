package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

// Hold the first change before commit so a second ownership change must wait.
type heldAudit struct {
	entered chan struct{}
	release chan struct{}
}

func (a heldAudit) CreateEvent(ctx context.Context, _ eventssvc.CreateEventParams) (*model.Event, error) {
	close(a.entered)
	select {
	case <-a.release:
		return &model.Event{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConcurrentOwnershipChangesPreserveActiveAdmin(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	for _, operation := range []string{"Remove", "Demote"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			pool := e2edb.OpenPostgresPool(t, "membership ownership")
			org := slug.Slug(fmt.Sprintf("ownership-%d", time.Now().UnixNano()))
			alice, bob, removed := org+"-alice", org+"-bob", org+"-removed"
			_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
				require.NoError(t, cleanupErr)
				_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM users WHERE slug IN ($1, $2, $3)`, alice, bob, removed)
				require.NoError(t, cleanupErr)
			})
			_, err = pool.Exec(ctx, `INSERT INTO roles (organization_id, slug, name)
				SELECT id, role, role FROM organizations CROSS JOIN unnest(ARRAY['admin', 'viewer']) role WHERE slug = $1`, org)
			require.NoError(t, err)
			for _, user := range []slug.Slug{alice, bob, removed} {
				_, err = pool.Exec(ctx, `INSERT INTO users (slug, email, role) VALUES ($1, $2, 'user')`, user, user.String()+"@example.com")
				require.NoError(t, err)
				member, createErr := members.NewRepository().Create(ctx, pool, &members.CreateParams{OrganizationSlug: org, UserSlug: user, RoleSlug: "admin"})
				require.NoError(t, createErr)
				require.Equal(t, user.String()+"@example.com", member.UserEmail)
			}
			_, err = pool.Exec(ctx, `UPDATE users SET deleted_at = NOW() WHERE slug = $1`, removed)
			require.NoError(t, err)
			count, err := members.NewRepository().CountByOrganization(ctx, pool, org)
			require.NoError(t, err)
			require.EqualValues(t, 2, count)

			audit := heldAudit{entered: make(chan struct{}), release: make(chan struct{})}
			defer func() {
				select {
				case <-audit.release:
				default:
					close(audit.release)
				}
			}()
			roles := &fakes.RolesService{GetRoleFn: func(context.Context, slug.Slug, slug.Slug) (*model.OrgRole, error) {
				return &model.OrgRole{Slug: "viewer"}, nil
			}}
			first := memberssvc.New(postgres.NewTransactionManager(pool), members.NewRepository(), nil,
				organizations.NewRepository(), roles, nil, nil, eventtest.NewProducer(audit), "", nil)
			second := memberssvc.New(postgres.NewTransactionManager(pool), members.NewRepository(), memberinvitations.NewRepository(),
				organizations.NewRepository(), roles, userssvc.New(postgres.NewTransactionManager(pool), nil, users.NewRepository(), nil, nil, ""), &testutil.FakeEmailProducer{}, eventtest.NewProducer(nil), "", nil)
			change := func(ctx context.Context, svc memberssvc.Service, user slug.Slug) error {
				if operation == "Remove" {
					return svc.RemoveMember(ctx, org, user, model.UserEventActor(user))
				}
				_, err := svc.UpdateMember(ctx, org, user, model.UserEventActor(user), model.UpdateMember{Role: "viewer"})
				return err
			}
			finished := make(chan error, 1)
			go func() { finished <- change(ctx, first, alice) }()
			select {
			case <-audit.entered:
			case <-ctx.Done():
				t.Fatal("first ownership change did not reach its audit stage")
			}
			blocked, cancelBlocked := context.WithTimeout(ctx, 200*time.Millisecond)
			err = change(blocked, second, bob)
			cancelBlocked()
			require.ErrorIs(t, err, context.DeadlineExceeded)
			close(audit.release)
			require.NoError(t, <-finished)
			require.ErrorIs(t, change(ctx, second, bob), memberssvc.ErrCannotRemoveLastAdmin)
			count, err = members.NewRepository().CountByRole(ctx, pool, org, "admin")
			require.NoError(t, err)
			require.EqualValues(t, 1, count)

			invitation, err := second.CreateMemberInvitation(ctx, org, bob, model.UserEventActor(bob), model.NewMemberInvitation{Email: "next-owner@example.com", Role: "admin"})
			require.NoError(t, err)
			require.Equal(t, model.MemberInvitationStatusPending, invitation.Status)
			require.ErrorIs(t, change(ctx, second, bob), memberssvc.ErrCannotRemoveLastAdmin)
			revoked, err := second.RevokeMemberInvitation(ctx, org, invitation.Slug, model.UserEventActor(bob))
			require.NoError(t, err)
			require.Equal(t, model.MemberInvitationStatusRevoked, revoked.Status)

			// The remaining admin can transfer ownership to an active member, then leave.
			if operation == "Remove" {
				restored, createErr := second.CreateMember(ctx, org, model.UserEventActor(bob), model.NewMember{UserSlug: alice, Role: "admin"})
				require.NoError(t, createErr)
				require.Equal(t, alice, restored.UserSlug)
				require.Equal(t, slug.Slug("admin"), restored.Role)
			} else {
				_, err = second.UpdateMember(ctx, org, alice, model.UserEventActor(bob), model.UpdateMember{Role: "admin"})
				require.NoError(t, err)
			}
			require.NoError(t, second.RemoveMember(ctx, org, bob, model.UserEventActor(bob)))
			remaining, err := second.ListMembers(ctx, org, model.ListRequest{Limit: 10})
			require.NoError(t, err)
			// This fixture retains the revoked user's membership to exercise active-admin counting.
			require.Len(t, remaining.Items, 2)
			require.ElementsMatch(t, []slug.Slug{alice, removed}, []slug.Slug{remaining.Items[0].UserSlug, remaining.Items[1].UserSlug})
			for _, member := range remaining.Items {
				require.Equal(t, slug.Slug("admin"), member.Role)
			}
			require.ErrorIs(t, second.RemoveMember(ctx, org, alice, model.UserEventActor(alice)), memberssvc.ErrCannotRemoveLastAdmin)
		})
	}
}
