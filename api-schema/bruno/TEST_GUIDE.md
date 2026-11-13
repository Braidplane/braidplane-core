# Test Execution Guide

This guide shows the recommended test execution sequence and what each test validates.

## 🎯 Quick Test Run (Happy Path)

Execute these requests in order to test the complete user onboarding workflow, auth recovery flows, and query parameter execution:

### 1. Create a User
**File:** `Create a user.bru`<br>
**Method:** `POST /users`<br>
**Validates:**
- ✅ User registration works
- ✅ Password is hashed (not returned)
- ✅ Default role `user` is assigned
- ✅ Unique timestamp prevents conflicts

**Variables Set:**
- `userSlug`: Used for session login
- `userEmail`: Used for session login
- `userPassword`: Used for session login and updated after password reset

**Expected Result:** `201 Created`

```json
{
  "slug": "test-user-1234567890",
  "full_name": "Test User",
  "email": "test.user+1234567890@example.com",
  "role": "user",
  "locale": "en",
  "created_at": "...",
  "updated_at": "..."
}
```

---

### 2. Resend Verification Email
**File:** `authentication/Resend verification email.bru`
**Method:** `POST /auth/email/resend-verification`
**Validates:**
- ✅ Resend verification endpoint is reachable
- ✅ Generic success response is returned
- ✅ A fresh verification email is sent to Mailpit

**Variables Used:**
- `userEmail` (from step 1)

**Expected Result:** `204 No Content`

---

### 3. Verify Email Address
**File:** `authentication/Verify email address.bru`<br>
**Method:** `POST /auth/email/verify`<br>
**Validates:**
- ✅ Verification email is fetched from Mailpit for the created user
- ✅ Verification token is extracted and submitted to verify endpoint
- ✅ Account is activated for authentication

**Variables Used:**
- `userEmail` (from step 1)

**Variables Set:**
- `verificationToken`

**Expected Result:** `204 No Content`

---

### 4. Request Password Reset
**File:** `authentication/Request password reset.bru`
**Method:** `POST /auth/password/forgot`
**Validates:**
- ✅ Forgot-password endpoint is reachable
- ✅ Generic success response is returned
- ✅ Password reset email is sent to Mailpit

**Variables Used:**
- `userEmail` (from step 1)

**Expected Result:** `204 No Content`

---

### 5. Reset Password
**File:** `authentication/Reset password.bru`
**Method:** `POST /auth/password/reset`
**Validates:**
- ✅ Reset token is extracted from the latest Mailpit email
- ✅ Password reset succeeds with the extracted token
- ✅ `userPassword` is updated for subsequent login requests

**Variables Used:**
- `userEmail` (from step 1)

**Variables Set:**
- `passwordResetToken`
- `userPassword` (updated to the new password)

**Expected Result:** `204 No Content`

---

### 6. Create a Session
**File:** `Create a session.bru`<br>
**Method:** `POST /sessions`<br>
**Validates:**
- ✅ Authentication works
- ✅ Login is possible only after email verification
- ✅ Session cookie is set
- ✅ Password verification works
- ✅ User data returned (no password)

**Variables Used:**
- `userEmail` (from step 1)
- `userPassword` (from step 1 or updated in step 5)

**Variables Set:**
- `authenticatedUserSlug`: For reference

**Expected Result:** `200 OK` + `Set-Cookie` header

**🔐 Important:** Session cookie is stored automatically by Bruno for subsequent requests

---

### 4. Create an Organization
**File:** `organizations/Create an organization.bru`<br>
**Method:** `POST /organizations`<br>
**Validates:**
- ✅ **System-level authorization** works (no org context)
- ✅ OPA uses `role_permissions` from `generated.rego`
- ✅ User role `user` has `organizations:create` permission
- ✅ Default roles (admin, viewer) are created automatically
- ✅ User becomes admin member automatically

**Variables Set:**
- `orgSlug`: Critical for all subsequent org-scoped requests
- `orgName`: For reference

**Expected Result:** `201 Created`

**Authorization Flow:**
```
User (role: user) → OPA → role_permissions["user"] → ["organizations:create"] → ALLOW
```

---

### 5. List Roles
**File:** `roles/List roles.bru`<br>
**Method:** `GET /organizations/{slug}/roles`<br>
**Validates:**
- ✅ Default roles created during org creation
- ✅ Admin role has `*:*` wildcard permission
- ✅ Viewer role has read-only permissions
- ✅ Org-scoped authorization works

**Variables Used:**
- `orgSlug` (from step 3)

**Expected Result:** `200 OK` with array of 2+ roles

```json
[
  {
    "slug": "admin",
    "name": "Admin",
    "is_default": true,
    "permissions": [{"resource": "*", "action": "*"}]
  },
  {
    "slug": "viewer",
    "name": "Viewer",
    "is_default": true,
    "permissions": [...]
  }
]
```

---

### 6. Create a Custom Role
**File:** `roles/Create a role.bru`<br>
**Method:** `POST /organizations/{slug}/roles`<br>
**Validates:**
- ✅ Custom roles can be created
- ✅ Permissions are assigned correctly
- ✅ Transaction integrity (role + permissions)
- ✅ Admin authorization works

**Variables Used:**
- `orgSlug` (from step 3)

**Variables Set:**
- `roleSlug`: For role retrieval

**Expected Result:** `201 Created`

```json
{
  "slug": "editor",
  "name": "Editor",
  "is_default": false,
  "permissions": [
    {"resource": "members", "action": "read"},
    {"resource": "organizations", "action": "read"}
  ]
}
```

---

### 7. Directly Add an Existing Member
**File:** `members/Add a member to an organization.bru`<br>
**Method:** `POST /organizations/{slug}/members`<br>
**Validates:**
- Transaction-aware role validation
- Role must exist before member added
- No race conditions
- Member structure returned

**Variables Used:**
- `orgSlug` (from step 3)
- `userSlug` for an existing user that is not already an organization member

**Expected Result:** `201 Created`

**Transaction Flow:**
```
BEGIN TX
  → Validate role exists
  → Create member
COMMIT
```

**Note:** This endpoint directly adds an existing user by slug. It is not the email invitation workflow. Email invitations are verified by `task bruno-ci` in the `member-invitations` folder.

---

### 8. List Organization Members
**File:** `members/List organization members.bru`<br>
**Method:** `GET /organizations/{slug}/members`<br>
**Validates:**
- Active members are returned in the paginated member-list response
- Org creator is admin
- Pending invitations are not included as active members
- Role assignments are correct

**Variables Used:**
- `orgSlug` (from step 3)

**Expected Result:** `200 OK` with a `members` array

```json
{
  "members": [
    {
      "user_slug": "test-user-...",
      "email": "test.user+...@example.com",
      "role": "admin",
      "created_at": "...",
      "updated_at": "..."
    }
  ],
  "has_more": false,
  "next_cursor": null,
  "prev_cursor": null
}
```

---

### 8a. Member Invitation Lifecycle
**Folder:** `multi-tenant-isolation/member-invitations`
**Command:** `task bruno-ci`
**Validates:**
- Admins invite by email.
- Pending invitations are not active members and do not grant access.
- Pending invitees cannot administer invitations.
- Existing verified users accept invitations through the Core membership API.
- Resend and revoke keep raw tokens out of API responses.
- Email verification alone does not accept pending invitations or grant membership.

---

### 9. Create a SQL Data Source
**File:** `data-sources/Create PostgreSQL data source.bru` (or MySQL equivalent)<br>
**Method:** `POST /organizations/{slug}/data-sources`<br>
**Validates:**
- Data source creation for query execution
- Organization-scoped authorization for data source operations

**Variables Set:**
- `dataSourceSlug`

**Expected Result:** `201 Created`

---

### 10. Create Postgres Search Query
**File:** `queries/postgres/Create Postgres query.bru`<br>
**Method:** `POST /organizations/{slug}/data-sources/{ds_slug}/queries`<br>
**Validates:**
- Query definition creation with `{{param}}` placeholders
- Parameter metadata persistence

**Variables Set:**
- `postgresSearchQuerySlug`
- `querySlug`

**Expected Result:** `201 Created`

---

### 11. Execute Postgres Search Query
**File:** `queries/postgres/Execute Postgres query.bru`<br>
**Method:** `POST /organizations/{slug}/data-sources/{ds_slug}/queries/{query_slug}/execute`<br>
**Validates:**
- Parameter value passing via `parameters` map
- Query execution response contract

**Variables Used:**
- `orgSlug`
- `dataSourceSlug`
- `postgresSearchQuerySlug`

**Expected Result:** `200 OK`

---

### 12. Insert Query Flows + Query-Agnostic Endpoints
**Files:**
- `queries/postgres/Create Postgres insert query.bru`
- `queries/postgres/Execute Postgres insert query.bru`
- `queries/mysql/Create MySQL insert query.bru`
- `queries/mysql/Execute MySQL insert query.bru`
- `queries/mongodb/Create MongoDB insert query.bru`
- `queries/mongodb/Execute MongoDB insert query.bru`
- `queries/http/Create HTTP insert query.bru`
- `queries/http/Execute HTTP insert query.bru`
- `queries/List queries.bru`
- `queries/Get a query.bru`
- `queries/Update a query.bru`
- `queries/Delete a query.bru`

These requests cover both search and insert examples for every connector type, plus shared query-agnostic CRUD operations.

---

## 🧪 Additional Tests

### 8. Update User Profile
**File:** `Update user profile.bru`<br>
**Method:** `PATCH /users/profile`<br>
**Validates:**
- ✅ Authenticated users can update their profile
- ✅ Session-based authentication works
- ✅ Profile special case in OPA works

**Expected Result:** `200 OK`

**OPA Special Case:**
```rego
# Users can always update their own profile
allow if {
    input.resource == "profile"
    input.action == "update"
    input.resource_slug == input.user.slug
}
```

---

### 9. List User's Organizations
**File:** `organizations/List user's organizations.bru`<br>
**Method:** `GET /organizations`<br>
**Validates:**
- ✅ User sees only orgs they're a member of
- ✅ `member_role` is included
- ✅ Recently created org appears

**Expected Result:** `200 OK`

---

### 10. Get a Specific Role
**File:** `roles/Get a role.bru`<br>
**Method:** `GET /organizations/{slug}/roles/{role_slug}`<br>
**Validates:**
- ✅ Role retrieval by slug
- ✅ Permissions included
- ✅ Default roles accessible

**Variables Used:**
- `orgSlug`, `roleSlug`

**Expected Result:** `200 OK`

---

## 🐛 Error Case Testing

### Test: Duplicate User Creation
1. Run `Create a user` twice with same email
2. Expected: `409 Conflict` (slug will differ due to timestamp, but email constraint)

### Test: Invalid Role Assignment
1. Modify `members/Add a member to an organization.bru` body: `"role": "nonexistent"`
2. Expected: `400 Bad Request` - "role does not exist in organization"

### Test: Unauthorized Access
1. Create new Bruno environment without session cookie
2. Try `Create an organization`
3. Expected: `401 Unauthorized`

### Test: Permission Denied
1. Create user with `viewer` role
2. Try to create a role in org
3. Expected: `403 Forbidden`

---

## 📊 Test Coverage Summary

| Category | Coverage |
|----------|----------|
| **Authentication** | ✅ Registration, email verification, login, session management |
| **Authorization** | ✅ System-level (OPA role_permissions), Org-scoped (DB permissions) |
| **Users** | ✅ CRUD, profile updates, password security |
| **Organizations** | ✅ Create, list, member management |
| **Roles** | ✅ Default roles, custom roles, permissions |
| **Members** | ✅ Add, list, role validation |
| **Transactions** | ✅ Role validation in same TX as member creation |
| **OPA Policies** | ✅ Wildcard, resource-specific, ownership, profile special case |

---

## 🎬 Video Tutorial

*Coming soon: Watch a complete test run walkthrough*

---

## 💡 Tips

1. **Run in order** - Tests build on each other via variables
2. **Check variables** - Use Bruno's variable viewer to see captured values
3. **Read pre-request errors** - They tell you which prerequisite to run first
4. **Examine test results** - Each assertion validates specific behavior
5. **Reset between runs** - Create fresh test data each time (timestamps help)

---

## 🔄 Automated Test Run (CLI)

Use the repository-pinned Bruno CLI wrapper:

```bash
# Run every candidate-local collection with execution coverage checking.
task bruno-ci

# Rerun the isolation workflow after installing Bruno and generating Worktree environments.
(cd api-schema/bruno/multi-tenant-isolation && \
  bash ../../../scripts/bruno-toolchain.sh run _setup org1 org2 z-cross-org member-invitations --env Worktree -r)
```

---

## 📝 Test Maintenance

When API changes:
1. Update request body/params
2. Update test assertions
3. Update this guide
4. Verify examples still match responses
5. Update variable dependencies

---

Need help? Check the [README](README.md) for troubleshooting common issues.
